// Package tracker 实现内存中的版本血缘影响追踪：
// 维护“消费者沿当前有效引用能否到达废弃版本”的精确集合，
// 并在引用改绑时以局部增量方式更新该集合。
//
// 核心思路：为每个可达节点维护一条指向废弃版本的“凭证路径”
// （parentRef/parentNode 构成的有向无环证书链）。删除引用时，
// 只有凭证子树真正可能失联，对该子树做局部修复；新增引用时，
// 只有此前不可达的节点可能经由新边变得可达，沿反向引用做局部扩散。
// 因此单次 Rebind 的代价只随实际受影响的局部区域增长，
// 与图中无关大分支的规模无关。常驻空间为 O(节点数 + 引用数)。
package tracker

import (
	"fmt"
	"sort"
)

// NodeKind 区分节点类型：数据集版本或消费者。
type NodeKind int

const (
	Version NodeKind = iota
	Consumer
)

// Node 是一个图节点，以完整 URN 唯一标识。
type Node struct {
	URN  string
	Kind NodeKind
}

// Ref 是一条引用。Target 为空字符串表示该引用停用。
type Ref struct {
	ID     string
	Owner  string
	Target string
}

// Stats 记录初始化之后、由 Rebind 引发的局部更新工作量。
type Stats struct {
	NodeChecks uint64 // 访问/标记的节点次数
	RefChecks  uint64 // 扫描的引用次数
}

// refSet 是引用 ID 的有序集合：O(1) 增删，迭代顺序确定。
type refSet struct {
	order []string
	pos   map[string]int
}

func (s *refSet) add(id string) {
	if s.pos == nil {
		s.pos = make(map[string]int)
	}
	if _, ok := s.pos[id]; ok {
		return
	}
	s.pos[id] = len(s.order)
	s.order = append(s.order, id)
}

func (s *refSet) remove(id string) {
	i, ok := s.pos[id]
	if !ok {
		return
	}
	last := s.order[len(s.order)-1]
	s.order[i] = last
	s.pos[last] = i
	s.order = s.order[:len(s.order)-1]
	delete(s.pos, id)
}

// nodeSet 是节点 URN 的有序集合：O(1) 增删，迭代顺序确定。
type nodeSet struct {
	order []string
	pos   map[string]int
}

func (s *nodeSet) add(urn string) {
	if s.pos == nil {
		s.pos = make(map[string]int)
	}
	if _, ok := s.pos[urn]; ok {
		return
	}
	s.pos[urn] = len(s.order)
	s.order = append(s.order, urn)
}

func (s *nodeSet) remove(urn string) {
	i, ok := s.pos[urn]
	if !ok {
		return
	}
	last := s.order[len(s.order)-1]
	s.order[i] = last
	s.pos[last] = i
	s.order = s.order[:len(s.order)-1]
	delete(s.pos, urn)
}

// Tracker 保存追踪状态。仅支持顺序调用，不考虑并发。
type Tracker struct {
	deprecated string
	kind       map[string]NodeKind
	refs       map[string]*refState
	outRefs    map[string]*refSet // owner -> 有效引用 ID 集
	inRefs     map[string]*refSet // target -> 有效引用 ID 集
	reachable  map[string]bool
	parentRef  map[string]string // node -> 作为可达凭证的引用 ID
	parentNode map[string]string // node -> 凭证引用的 target 节点
	children   map[string]*nodeSet
	affected   map[string]struct{}
	flipLog    map[string]bool // 本次 Rebind 中翻转过的消费者 -> 翻转前是否受影响
	stats      Stats
}

type refState struct {
	id     string
	owner  string
	target string
}

// NewTracker 载入节点、引用与废弃版本，建立初始状态。
func NewTracker(nodes []Node, refs []Ref, deprecated string) (*Tracker, error) {
	t := &Tracker{
		deprecated: deprecated,
		kind:       make(map[string]NodeKind, len(nodes)),
		refs:       make(map[string]*refState, len(refs)),
		outRefs:    make(map[string]*refSet),
		inRefs:     make(map[string]*refSet),
		reachable:  make(map[string]bool, len(nodes)),
		parentRef:  make(map[string]string),
		parentNode: make(map[string]string),
		children:   make(map[string]*nodeSet),
		affected:   make(map[string]struct{}),
	}
	for _, n := range nodes {
		if _, dup := t.kind[n.URN]; dup {
			return nil, fmt.Errorf("duplicate node URN %q", n.URN)
		}
		t.kind[n.URN] = n.Kind
	}
	k, ok := t.kind[deprecated]
	if !ok {
		return nil, fmt.Errorf("deprecated node %q not found", deprecated)
	}
	if k != Version {
		return nil, fmt.Errorf("deprecated node %q is not a version", deprecated)
	}
	for _, r := range refs {
		if _, dup := t.refs[r.ID]; dup {
			return nil, fmt.Errorf("duplicate ref ID %q", r.ID)
		}
		if _, ok := t.kind[r.Owner]; !ok {
			return nil, fmt.Errorf("ref %q has unknown owner %q", r.ID, r.Owner)
		}
		if r.Target != "" {
			tk, ok := t.kind[r.Target]
			if !ok {
				return nil, fmt.Errorf("ref %q has unknown target %q", r.ID, r.Target)
			}
			if tk != Version {
				return nil, fmt.Errorf("ref %q target %q is not a version", r.ID, r.Target)
			}
		}
		t.refs[r.ID] = &refState{id: r.ID, owner: r.Owner, target: r.Target}
		if r.Target != "" {
			t.addToIndex(t.refs[r.ID])
		}
	}
	t.buildInitial()
	return t, nil
}

func (t *Tracker) refSetFor(m map[string]*refSet, key string) *refSet {
	s := m[key]
	if s == nil {
		s = &refSet{}
		m[key] = s
	}
	return s
}

func (t *Tracker) addToIndex(r *refState) {
	t.refSetFor(t.outRefs, r.owner).add(r.id)
	t.refSetFor(t.inRefs, r.target).add(r.id)
}

func (t *Tracker) removeFromIndex(r *refState) {
	if s := t.outRefs[r.owner]; s != nil {
		s.remove(r.id)
	}
	if s := t.inRefs[r.target]; s != nil {
		s.remove(r.id)
	}
}

// buildInitial 从废弃版本沿反向引用做一次 BFS，建立初始凭证树。
// 初始化允许全图扫描；此后的 Rebind 不做全图工作。
func (t *Tracker) buildInitial() {
	t.reachable[t.deprecated] = true
	queue := []string{t.deprecated}
	for len(queue) > 0 {
		y := queue[0]
		queue = queue[1:]
		s := t.inRefs[y]
		if s == nil {
			continue
		}
		for _, id := range s.order {
			w := t.refs[id].owner
			if t.reachable[w] {
				continue
			}
			t.reach(w, y, id)
			queue = append(queue, w)
		}
	}
}

// reach 将 w 标记为可达，凭证为经引用 refID 指向 p。
func (t *Tracker) reach(w, p, refID string) {
	t.reachable[w] = true
	t.parentRef[w] = refID
	t.parentNode[w] = p
	s := t.children[p]
	if s == nil {
		s = &nodeSet{}
		t.children[p] = s
	}
	s.add(w)
	if t.kind[w] == Consumer {
		t.flip(w)
	}
}

// unreach 将 w 标记为不可达并摘除凭证。
func (t *Tracker) unreach(w string) {
	t.reachable[w] = false
	if p, ok := t.parentNode[w]; ok {
		if s := t.children[p]; s != nil {
			s.remove(w)
		}
	}
	delete(t.parentRef, w)
	delete(t.parentNode, w)
	if t.kind[w] == Consumer {
		t.flip(w)
	}
}

// flip 翻转消费者在受影响集合中的状态，并记录翻转前的原始状态。
func (t *Tracker) flip(w string) {
	if t.flipLog != nil {
		if _, logged := t.flipLog[w]; !logged {
			_, was := t.affected[w]
			t.flipLog[w] = was
		}
	}
	if _, in := t.affected[w]; in {
		delete(t.affected, w)
	} else {
		t.affected[w] = struct{}{}
	}
}

// Rebind 的实现见 rebind.go。

// Impact 返回当前全部受影响消费者的 URN 集合（有序）。
func (t *Tracker) Impact() []string {
	out := make([]string, 0, len(t.affected))
	for urn := range t.affected {
		out = append(out, urn)
	}
	sort.Strings(out)
	return out
}

// Stats 返回 Rebind 局部更新的累计检查计数（不含初始化）。
func (t *Tracker) Stats() Stats {
	return t.stats
}

// ResetStats 清零检查计数。
func (t *Tracker) ResetStats() {
	t.stats = Stats{}
}
