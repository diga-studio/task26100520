package lineage

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// bruteImpact 全量计算：从废弃版本沿有效引用反向可达的消费者集合。
// 作为独立 oracle 核对 Tracker 的增量结果。
func bruteImpact(nodes []Node, refs []Ref, deprecated string) []string {
	idx := make(map[string]int, len(nodes))
	kind := make([]NodeKind, len(nodes))
	for i, n := range nodes {
		idx[n.URN] = i
		kind[i] = n.Kind
	}
	// 反向邻接：target -> owners
	preds := make(map[int][]int)
	for _, r := range refs {
		if r.Target == "" {
			continue
		}
		preds[idx[r.Target]] = append(preds[idx[r.Target]], idx[r.Owner])
	}
	dep := idx[deprecated]
	seen := make([]bool, len(nodes))
	stack := []int{dep}
	seen[dep] = true
	for len(stack) > 0 {
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, p := range preds[v] {
			if !seen[p] {
				seen[p] = true
				stack = append(stack, p)
			}
		}
	}
	var out []string
	for i, n := range nodes {
		if kind[i] == Consumer && seen[i] {
			out = append(out, n.URN)
		}
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

func diffSets(before, after []string) (added, removed []string) {
	inBefore := make(map[string]bool, len(before))
	for _, s := range before {
		inBefore[s] = true
	}
	inAfter := make(map[string]bool, len(after))
	for _, s := range after {
		inAfter[s] = true
		if !inBefore[s] {
			added = append(added, s)
		}
	}
	for _, s := range before {
		if !inAfter[s] {
			removed = append(removed, s)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// 随机小图上执行连续改绑序列，每步用全量 oracle 核对
// Impact 与 Rebind 差集。
func TestRandomSmallGraphs(t *testing.T) {
	for trial := 0; trial < 40; trial++ {
		rng := rand.New(rand.NewSource(int64(trial) + 1))
		nV := 3 + rng.Intn(8)  // 版本数
		nC := 2 + rng.Intn(6)  // 消费者数
		nR := 4 + rng.Intn(20) // 引用数
		var nodes []Node
		var versions []string
		for i := 0; i < nV; i++ {
			urn := fmt.Sprintf("urn:ds:%d:%d", trial, i)
			nodes = append(nodes, Node{URN: urn, Kind: Version})
			versions = append(versions, urn)
		}
		for i := 0; i < nC; i++ {
			nodes = append(nodes, Node{URN: fmt.Sprintf("urn:c:%d:%d", trial, i), Kind: Consumer})
		}
		dep := versions[rng.Intn(len(versions))]
		var refs []Ref
		for i := 0; i < nR; i++ {
			owner := nodes[rng.Intn(len(nodes))].URN
			target := ""
			if rng.Intn(4) > 0 { // 3/4 概率启用
				target = versions[rng.Intn(len(versions))]
			}
			refs = append(refs, Ref{ID: fmt.Sprintf("r%d", i), Owner: owner, Target: target})
		}

		tr, err := NewTracker(nodes, refs, dep)
		if err != nil {
			t.Fatalf("trial %d: NewTracker: %v", trial, err)
		}
		prev := bruteImpact(nodes, refs, dep)
		checkImpact(t, tr, prev)

		for step := 0; step < 60; step++ {
			ri := rng.Intn(len(refs))
			target := ""
			if rng.Intn(4) > 0 {
				target = versions[rng.Intn(len(versions))]
			}
			added, removed, err := tr.Rebind(refs[ri].ID, target)
			if err != nil {
				t.Fatalf("trial %d step %d: Rebind: %v", trial, step, err)
			}
			refs[ri].Target = target

			after := bruteImpact(nodes, refs, dep)
			wantAdded, wantRemoved := diffSets(prev, after)
			if got := tr.Impact(); !equalStrSet(got, after) {
				t.Fatalf("trial %d step %d: Impact = %v, oracle = %v",
					trial, step, got, after)
			}
			if !equalStrSet(added, wantAdded) || !equalStrSet(removed, wantRemoved) {
				t.Fatalf("trial %d step %d: diff = (+%v, -%v), oracle = (+%v, -%v)",
					trial, step, added, removed, wantAdded, wantRemoved)
			}
			prev = after
		}
	}
}

func equalStrSet(a, b []string) bool {
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	if len(as) != len(bs) {
		return false
	}
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}
