package tracker

import (
	"fmt"
	"sort"
)

// Rebind 将指定引用的 target 替换为 newTarget（空串表示停用），
// 返回本次新增受影响与不再受影响的消费者 URN 集合（有序、去重）。
// 返回值为替换前后两个状态的集合差：改绑过程中暂时消失又恢复的
// 消费者不会被报告。
func (t *Tracker) Rebind(refID, newTarget string) (added, removed []string, err error) {
	r, ok := t.refs[refID]
	if !ok {
		return nil, nil, fmt.Errorf("unknown ref %q", refID)
	}
	if newTarget != "" {
		k, ok := t.kind[newTarget]
		if !ok {
			return nil, nil, fmt.Errorf("unknown target node %q", newTarget)
		}
		if k != Version {
			return nil, nil, fmt.Errorf("target %q is not a version", newTarget)
		}
	}
	old := r.target
	if old == newTarget {
		return []string{}, []string{}, nil
	}
	t.flipLog = make(map[string]bool)
	if old != "" {
		t.removeFromIndex(r)
		t.deleteEdge(r.owner, old, r.id)
	}
	r.target = newTarget
	if newTarget != "" {
		t.addToIndex(r)
		t.insertEdge(r)
	}
	for w, was := range t.flipLog {
		_, now := t.affected[w]
		switch {
		case was && !now:
			removed = append(removed, w)
		case !was && now:
			added = append(added, w)
		}
	}
	t.flipLog = nil
	if added == nil {
		added = []string{}
	}
	if removed == nil {
		removed = []string{}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed, nil
}

// deleteEdge 处理引用 (u -> tgt)（引用 ID refID）被移除后的局部修复。
// 只有 u 的可达凭证恰好是该引用时，其凭证子树才可能失联。
func (t *Tracker) deleteEdge(u, tgt, refID string) {
	t.stats.NodeChecks++
	if t.parentRef[u] != refID {
		return
	}
	// 同 owner->target 的重复引用可直接顶替凭证。
	if s := t.outRefs[u]; s != nil {
		for _, id := range s.order {
			t.stats.RefChecks++
			if alt := t.refs[id]; alt.target == tgt {
				t.parentRef[u] = alt.id
				return
			}
		}
	}
	// 收集 u 的凭证子树 D：只有这些节点的凭证路径经过被删引用。
	inD := map[string]bool{u: true}
	nodes := []string{u}
	stack := []string{u}
	for len(stack) > 0 {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if s := t.children[x]; s != nil {
			for _, ch := range s.order {
				t.stats.NodeChecks++
				if !inD[ch] {
					inD[ch] = true
					nodes = append(nodes, ch)
					stack = append(stack, ch)
				}
			}
		}
	}
	for _, x := range nodes {
		t.unreach(x)
	}
	// 修复第一阶段：D 中节点若有指向 D 外可达节点的引用，直接重新挂接。
	var queue []string
	for _, x := range nodes {
		t.stats.NodeChecks++
		if s := t.outRefs[x]; s != nil {
			for _, id := range s.order {
				t.stats.RefChecks++
				if r2 := t.refs[id]; t.reachable[r2.target] {
					t.reach(x, r2.target, r2.id)
					queue = append(queue, x)
					break
				}
			}
		}
	}
	// 修复第二阶段：沿反向引用在 D 内扩散。
	for len(queue) > 0 {
		y := queue[0]
		queue = queue[1:]
		t.stats.NodeChecks++
		if s := t.inRefs[y]; s != nil {
			for _, id := range s.order {
				t.stats.RefChecks++
				if w := t.refs[id].owner; inD[w] && !t.reachable[w] {
					t.reach(w, y, id)
					queue = append(queue, w)
				}
			}
		}
	}
}

// insertEdge 处理新增引用 r。只有此前不可达的 owner 才可能
// 经由新边变得可达，并沿反向引用扩散到其它不可达节点。
func (t *Tracker) insertEdge(r *refState) {
	t.stats.NodeChecks++
	if t.reachable[r.owner] || !t.reachable[r.target] {
		return
	}
	t.reach(r.owner, r.target, r.id)
	queue := []string{r.owner}
	for len(queue) > 0 {
		y := queue[0]
		queue = queue[1:]
		t.stats.NodeChecks++
		if s := t.inRefs[y]; s != nil {
			for _, id := range s.order {
				t.stats.RefChecks++
				if w := t.refs[id].owner; !t.reachable[w] {
					t.reach(w, y, id)
					queue = append(queue, w)
				}
			}
		}
	}
}
