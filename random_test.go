package tracker

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

// bruteForce 独立的全量计算：从废弃版本沿当前有效引用反向 BFS，
// 收集所有可达消费者。用于核对增量维护的结果。
func bruteForce(nodes []Node, refs []Ref, deprecated string) []string {
	incoming := map[string][]string{}
	for _, r := range refs {
		if r.Target != "" {
			incoming[r.Target] = append(incoming[r.Target], r.Owner)
		}
	}
	isConsumer := map[string]bool{}
	for _, n := range nodes {
		if n.Kind == Consumer {
			isConsumer[n.URN] = true
		}
	}
	seen := map[string]bool{deprecated: true}
	queue := []string{deprecated}
	var out []string
	for len(queue) > 0 {
		y := queue[0]
		queue = queue[1:]
		if isConsumer[y] {
			out = append(out, y)
		}
		for _, w := range incoming[y] {
			if !seen[w] {
				seen[w] = true
				queue = append(queue, w)
			}
		}
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

func diffSets(before, after []string) (added, removed []string) {
	inB, inA := map[string]bool{}, map[string]bool{}
	for _, x := range before {
		inB[x] = true
	}
	for _, x := range after {
		inA[x] = true
		if !inB[x] {
			added = append(added, x)
		}
	}
	for _, x := range before {
		if !inA[x] {
			removed = append(removed, x)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	if added == nil {
		added = []string{}
	}
	if removed == nil {
		removed = []string{}
	}
	return added, removed
}

// 随机小图上的连续改绑序列：每步用全量计算核对 Impact 与 Rebind 差集。
func TestRandomRebindSequences(t *testing.T) {
	for seed := int64(0); seed < 60; seed++ {
		rng := rand.New(rand.NewSource(seed))
		nV := 3 + rng.Intn(6) // 版本数（含废弃版本）
		nC := 2 + rng.Intn(5) // 消费者数
		var nodes []Node
		var versions []string
		dep := "v0"
		for i := 0; i < nV; i++ {
			urn := fmt.Sprintf("v%d", i)
			versions = append(versions, urn)
			nodes = append(nodes, v(urn))
		}
		for i := 0; i < nC; i++ {
			urn := fmt.Sprintf("c%d", i)
			nodes = append(nodes, c(urn))
		}
		nRef := 5 + rng.Intn(20)
		refs := make([]Ref, nRef)
		for i := range refs {
			owner := nodes[rng.Intn(len(nodes))].URN
			target := ""
			if rng.Intn(4) > 0 { // 1/4 概率初始停用
				target = versions[rng.Intn(len(versions))]
			}
			refs[i] = Ref{ID: fmt.Sprintf("r%d", i), Owner: owner, Target: target}
		}
		tr, err := NewTracker(nodes, refs, dep)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if got, want := tr.Impact(), bruteForce(nodes, refs, dep); !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d: initial Impact = %v, want %v", seed, got, want)
		}
		for step := 0; step < 150; step++ {
			before := bruteForce(nodes, refs, dep)
			idx := rng.Intn(len(refs))
			newTarget := ""
			if rng.Intn(4) > 0 {
				newTarget = versions[rng.Intn(len(versions))]
			}
			added, removed, err := tr.Rebind(refs[idx].ID, newTarget)
			if err != nil {
				t.Fatalf("seed %d step %d: %v", seed, step, err)
			}
			refs[idx].Target = newTarget
			after := bruteForce(nodes, refs, dep)
			wantAdded, wantRemoved := diffSets(before, after)
			if !reflect.DeepEqual(added, wantAdded) || !reflect.DeepEqual(removed, wantRemoved) {
				t.Fatalf("seed %d step %d: Rebind(%s -> %q) diff = (+%v, -%v), want (+%v, -%v)",
					seed, step, refs[idx].ID, newTarget, added, removed, wantAdded, wantRemoved)
			}
			if got := tr.Impact(); !reflect.DeepEqual(got, after) {
				t.Fatalf("seed %d step %d: Impact = %v, want %v", seed, step, got, after)
			}
		}
	}
}
