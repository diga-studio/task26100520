// Package lineage 实现内存中的版本血缘影响追踪：
// 维护“消费者沿当前有效引用能否到达废弃版本”的集合，
// 支持局部改绑（Rebind）后的增量更新。
//
// 不变式：消费者 c 受影响 当且仅当 从 c 沿当前有效引用可到达废弃版本。
// 内部为每个节点维护 dist（到废弃版本的最短距离，-1 表示不可达），
// 受影响即 dist 有限。Rebind 只在“改绑引用 owner 的祖先区域”内重算 dist，
// 不触碰无关分支。
package lineage

import (
	"container/heap"
	"errors"
	"fmt"
	"sort"
)

// NodeKind 区分节点类型：数据集版本或消费者。
type NodeKind int

const (
	Version  NodeKind = iota // 数据集版本
	Consumer                 // 消费者
)

// Node 是血缘图中的一个节点，以完整 URN 唯一标识。
type Node struct {
	URN  string
	Kind NodeKind
}

// Ref 是一条引用：owner 指向 target。
// owner 可以是版本或消费者；target 只能是版本，空字符串表示该引用停用。
type Ref struct {
	ID     string
	Owner  string
	Target string
}

// Stats 记录初始化之后增量更新过程中的检查计数，
// 用于验证局部改绑的工作量不随无关分支规模增长。
type Stats struct {
	NodesVisited int64 // 祖先回扫与距离重算中访问的节点数
	EdgesScanned int64 // 上述过程中扫描的引用数
}

const unreachable = -1

// refRec 是引用的内部记录，同时挂在 owner 的出边单链表
// 和 target 的入边双链表上（改绑时 O(1) 摘挂）。
type refRec struct {
	owner  int
	target int // -1 表示停用
	id     string

	outNext int // 同一 owner 的下一条出边（ref 下标，-1 结束）

	inPrev int // 同一 target 的入边双链表
	inNext int
}

// Tracker 维护血缘图与受影响消费者集合。
type Tracker struct {
	urns    []string
	kind    []NodeKind
	nodeIdx map[string]int

	refs    []refRec
	refIdx  map[string]int
	outHead []int // 每个节点的出边链表头
	inHead  []int // 每个节点的入边链表头

	dist       []int // 到废弃版本的最短距离，unreachable 表示不可达
	deprecated int

	affected map[int]bool // 当前受影响的消费者下标

	// 复用的工作区，避免每次 Rebind 分配
	stamp   []int // 区域标记（epoch 法）
	epoch   int
	heapBuf distHeap

	stats Stats
}

// NewTracker 载入节点、引用和废弃版本，建立初始状态。
func NewTracker(nodes []Node, refs []Ref, deprecated string) (*Tracker, error) {
	t := &Tracker{
		nodeIdx:  make(map[string]int, len(nodes)),
		refIdx:   make(map[string]int, len(refs)),
		affected: make(map[int]bool),
	}
	for _, n := range nodes {
		if n.URN == "" {
			return nil, errors.New("lineage: 节点 URN 不能为空")
		}
		if _, dup := t.nodeIdx[n.URN]; dup {
			return nil, fmt.Errorf("lineage: 节点 URN 重复: %s", n.URN)
		}
		t.nodeIdx[n.URN] = len(t.urns)
		t.urns = append(t.urns, n.URN)
		t.kind = append(t.kind, n.Kind)
	}
	dep, ok := t.nodeIdx[deprecated]
	if !ok {
		return nil, fmt.Errorf("lineage: 废弃版本不存在: %s", deprecated)
	}
	if t.kind[dep] != Version {
		return nil, fmt.Errorf("lineage: 废弃节点必须是版本: %s", deprecated)
	}
	t.deprecated = dep

	n := len(t.urns)
	t.outHead = make([]int, n)
	t.inHead = make([]int, n)
	t.dist = make([]int, n)
	t.stamp = make([]int, n)
	for i := range t.outHead {
		t.outHead[i] = -1
		t.inHead[i] = -1
		t.dist[i] = unreachable
	}

	for _, r := range refs {
		if r.ID == "" {
			return nil, errors.New("lineage: 引用 ID 不能为空")
		}
		if _, dup := t.refIdx[r.ID]; dup {
			return nil, fmt.Errorf("lineage: 引用 ID 重复: %s", r.ID)
		}
		owner, ok := t.nodeIdx[r.Owner]
		if !ok {
			return nil, fmt.Errorf("lineage: 引用 %s 的 owner 不存在: %s", r.ID, r.Owner)
		}
		target := -1
		if r.Target != "" {
			ti, ok := t.nodeIdx[r.Target]
			if !ok {
				return nil, fmt.Errorf("lineage: 引用 %s 的 target 不存在: %s", r.ID, r.Target)
			}
			if t.kind[ti] != Version {
				return nil, fmt.Errorf("lineage: 引用 %s 的 target 必须是版本: %s", r.ID, r.Target)
			}
			target = ti
		}
		idx := len(t.refs)
		t.refIdx[r.ID] = idx
		t.refs = append(t.refs, refRec{
			owner: owner, target: target, id: r.ID,
			outNext: -1, inPrev: -1, inNext: -1,
		})
		// 挂到 owner 出边链表
		t.refs[idx].outNext = t.outHead[owner]
		t.outHead[owner] = idx
		// 挂到 target 入边链表
		if target >= 0 {
			t.linkIn(idx, target)
		}
	}

	// 初始全量计算：从废弃版本沿入边反向做多源 Dijkstra。
	t.dist[dep] = 0
	h := &t.heapBuf
	heap.Push(h, distItem{node: dep, dist: 0})
	for h.Len() > 0 {
		it := heap.Pop(h).(distItem)
		if it.dist != t.dist[it.node] {
			continue
		}
		for ri := t.inHead[it.node]; ri >= 0; ri = t.refs[ri].inNext {
			p := t.refs[ri].owner
			if t.dist[p] == unreachable || t.dist[p] > it.dist+1 {
				t.dist[p] = it.dist + 1
				heap.Push(h, distItem{node: p, dist: it.dist + 1})
			}
		}
	}
	for v := range t.urns {
		if t.kind[v] == Consumer && t.dist[v] != unreachable {
			t.affected[v] = true
		}
	}
	return t, nil
}

// linkIn 把引用挂到 target 的入边双链表头部。
func (t *Tracker) linkIn(ri, target int) {
	r := &t.refs[ri]
	r.inPrev = -1
	r.inNext = t.inHead[target]
	if r.inNext >= 0 {
		t.refs[r.inNext].inPrev = ri
	}
	t.inHead[target] = ri
}

// unlinkIn 把引用从其当前 target 的入边双链表摘下。
func (t *Tracker) unlinkIn(ri int) {
	r := &t.refs[ri]
	if r.inPrev >= 0 {
		t.refs[r.inPrev].inNext = r.inNext
	} else {
		t.inHead[r.target] = r.inNext
	}
	if r.inNext >= 0 {
		t.refs[r.inNext].inPrev = r.inPrev
	}
	r.inPrev, r.inNext = -1, -1
}

// Rebind 将指定引用的 target 替换为另一个版本 URN 或置空（停用），
// 返回本次新增受影响和不再受影响的消费者 URN 集合。
// 停用的引用可以用同样的方式重新启用。
func (t *Tracker) Rebind(refID, newTarget string) (added, removed []string, err error) {
	ri, ok := t.refIdx[refID]
	if !ok {
		return nil, nil, fmt.Errorf("lineage: 引用不存在: %s", refID)
	}
	newIdx := -1
	if newTarget != "" {
		ti, ok := t.nodeIdx[newTarget]
		if !ok {
			return nil, nil, fmt.Errorf("lineage: 新 target 不存在: %s", newTarget)
		}
		if t.kind[ti] != Version {
			return nil, nil, fmt.Errorf("lineage: 新 target 必须是版本: %s", newTarget)
		}
		newIdx = ti
	}
	r := &t.refs[ri]
	if r.target == newIdx {
		return nil, nil, nil // 无变化
	}
	owner := r.owner

	// 第 1 步：在旧图上回扫 owner 的祖先区域（dist 可能受影响的节点集合）。
	t.epoch++
	t.markAncestors(owner)

	// 第 2 步：应用改绑。
	if r.target >= 0 {
		t.unlinkIn(ri)
	}
	r.target = newIdx
	if newIdx >= 0 {
		t.linkIn(ri, newIdx)
	}

	// 第 3 步：在新图上再回扫一次祖先区域，与旧区域取并集
	// （改绑可能改变“谁能到达 owner”，两侧节点的 dist 都可能变化）。
	t.markAncestors(owner)

	// 第 4 步：在区域内重算 dist。区域外的节点 dist 不变，
	// 作为边界；废弃版本恒为 0，同样作为边界。
	region := t.collectRegion()
	oldDist := make([]int, len(region))
	newDist := make([]int, len(region))
	h := &t.heapBuf
	for i, v := range region {
		oldDist[i] = t.dist[v]
		best := unreachable
		if v == t.deprecated {
			best = 0 // 废弃版本恒为边界
		} else {
			for oi := t.outHead[v]; oi >= 0; oi = t.refs[oi].outNext {
				t.stats.EdgesScanned++
				tg := t.refs[oi].target
				if tg < 0 || t.stamp[tg] == t.epoch {
					continue // 停用或指向区域内，交给 Dijkstra
				}
				if t.dist[tg] != unreachable && (best == unreachable || t.dist[tg]+1 < best) {
					best = t.dist[tg] + 1
				}
			}
		}
		newDist[i] = best
		if best != unreachable {
			heap.Push(h, distItem{node: v, dist: best})
		}
		t.stats.NodesVisited++
	}
	// 区域下标 -> region 切片下标，用于 Dijkstra 松弛时定位。
	pos := make(map[int]int, len(region))
	for i, v := range region {
		pos[v] = i
	}
	for h.Len() > 0 {
		it := heap.Pop(h).(distItem)
		i := pos[it.node]
		if it.dist != newDist[i] {
			continue
		}
		t.stats.NodesVisited++
		for ei := t.inHead[it.node]; ei >= 0; ei = t.refs[ei].inNext {
			t.stats.EdgesScanned++
			p := t.refs[ei].owner
			if t.stamp[p] != t.epoch {
				continue // 区域外节点不受影响
			}
			pi := pos[p]
			if newDist[pi] == unreachable || newDist[pi] > it.dist+1 {
				newDist[pi] = it.dist + 1
				heap.Push(h, distItem{node: p, dist: it.dist + 1})
			}
		}
	}

	// 第 5 步：写回并计算消费者差集。
	for i, v := range region {
		was := oldDist[i] != unreachable
		is := newDist[i] != unreachable
		t.dist[v] = newDist[i]
		if t.kind[v] != Consumer || was == is {
			continue
		}
		if is {
			t.affected[v] = true
			added = append(added, t.urns[v])
		} else {
			delete(t.affected, v)
			removed = append(removed, t.urns[v])
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed, nil
}

// markAncestors 从 owner 沿入边反向 BFS，把可达节点打上当前 epoch 标记。
// 废弃版本不打标记：它恒为 dist=0 的边界，不参与区域重算。
func (t *Tracker) markAncestors(owner int) {
	if owner != t.deprecated && t.stamp[owner] != t.epoch {
		t.stamp[owner] = t.epoch
		t.stats.NodesVisited++
	}
	queue := []int{owner}
	for len(queue) > 0 {
		v := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for ri := t.inHead[v]; ri >= 0; ri = t.refs[ri].inNext {
			t.stats.EdgesScanned++
			p := t.refs[ri].owner
			if p != t.deprecated && t.stamp[p] != t.epoch {
				t.stamp[p] = t.epoch
				t.stats.NodesVisited++
				queue = append(queue, p)
			}
		}
	}
}

// collectRegion 收集当前 epoch 标记的节点，并排除废弃版本（恒为边界）。
func (t *Tracker) collectRegion() []int {
	var region []int
	for v := range t.urns {
		if t.stamp[v] == t.epoch && v != t.deprecated {
			region = append(region, v)
		}
	}
	return region
}

// Impact 返回当前全部受影响消费者的 URN 集合。
func (t *Tracker) Impact() []string {
	out := make([]string, 0, len(t.affected))
	for v := range t.affected {
		out = append(out, t.urns[v])
	}
	sort.Strings(out)
	return out
}

// Stats 返回自上次 ResetStats 以来累计的检查计数。
func (t *Tracker) Stats() Stats {
	return t.stats
}

// ResetStats 清零检查计数。
func (t *Tracker) ResetStats() {
	t.stats = Stats{}
}

// distItem 是 Dijkstra 堆元素。
type distItem struct {
	node int
	dist int
}

// distHeap 实现 heap.Interface。
type distHeap []distItem

func (h distHeap) Len() int           { return len(h) }
func (h distHeap) Less(i, j int) bool { return h[i].dist < h[j].dist }
func (h distHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *distHeap) Push(x any)        { *h = append(*h, x.(distItem)) }
func (h *distHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}
