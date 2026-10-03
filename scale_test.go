package tracker

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

const (
	smallVersions  = 15
	smallConsumers = 15
	smallRefs      = 100
	bgVersions     = 100000
	bgConsumers    = 100000
	rebindRounds   = 1000
)

// buildGraph 构造小分支（30 节点、100 条引用）与可选的背景分支
// （20 万节点、100 万条引用），两分支只共享废弃源。
func buildGraph(withBackground bool) (nodes []Node, refs []Ref, smallRefIDs []string, bgConsumerURNs []string) {
	dep := "urn:ds:deprecated:v1"
	nodes = append(nodes, v(dep))
	rng := rand.New(rand.NewSource(42))
	var smallVers []string
	for i := 0; i < smallVersions; i++ {
		urn := fmt.Sprintf("urn:ds:small:v%d", i)
		smallVers = append(smallVers, urn)
		nodes = append(nodes, v(urn))
	}
	for i := 0; i < smallConsumers; i++ {
		nodes = append(nodes, c(fmt.Sprintf("urn:consumer:small:%d", i)))
	}
	targets := append(append([]string{}, smallVers...), dep)
	for i := 0; i < smallRefs; i++ {
		var owner string
		if i < 60 {
			owner = smallVers[rng.Intn(len(smallVers))]
		} else {
			owner = fmt.Sprintf("urn:consumer:small:%d", rng.Intn(smallConsumers))
		}
		target := ""
		if rng.Intn(5) > 0 {
			target = targets[rng.Intn(len(targets))]
		}
		id := fmt.Sprintf("small-ref-%d", i)
		smallRefIDs = append(smallRefIDs, id)
		refs = append(refs, Ref{ID: id, Owner: owner, Target: target})
	}
	if !withBackground {
		return nodes, refs, smallRefIDs, nil
	}
	// 背景分支：bv0 经 6 条重复引用连到废弃源，其余版本各 6 条指向更早版本，
	// 每个背景消费者 4 条指向随机背景版本。合计恰好 1,000,000 条引用。
	bgVers := make([]string, bgVersions)
	for i := 0; i < bgVersions; i++ {
		bgVers[i] = fmt.Sprintf("urn:ds:bg:v%d", i)
		nodes = append(nodes, v(bgVers[i]))
	}
	refID := 0
	for i := 0; i < bgVersions; i++ {
		for k := 0; k < 6; k++ {
			target := dep
			if i > 0 {
				target = bgVers[rng.Intn(i)]
			}
			refs = append(refs, Ref{ID: fmt.Sprintf("bg-ref-%d", refID), Owner: bgVers[i], Target: target})
			refID++
		}
	}
	for i := 0; i < bgConsumers; i++ {
		urn := fmt.Sprintf("urn:consumer:bg:%d", i)
		nodes = append(nodes, c(urn))
		bgConsumerURNs = append(bgConsumerURNs, urn)
		for k := 0; k < 4; k++ {
			refs = append(refs, Ref{ID: fmt.Sprintf("bg-ref-%d", refID), Owner: urn, Target: bgVers[rng.Intn(bgVersions)]})
			refID++
		}
	}
	return nodes, refs, smallRefIDs, bgConsumerURNs
}

type rebindOp struct {
	refID  string
	target string
}

func genOps(smallRefIDs []string) []rebindOp {
	rng := rand.New(rand.NewSource(7))
	dep := "urn:ds:deprecated:v1"
	var smallVers []string
	for i := 0; i < smallVersions; i++ {
		smallVers = append(smallVers, fmt.Sprintf("urn:ds:small:v%d", i))
	}
	ops := make([]rebindOp, rebindRounds)
	for i := range ops {
		target := ""
		switch rng.Intn(5) {
		case 0:
			target = ""
		case 1:
			target = dep
		default:
			target = smallVers[rng.Intn(len(smallVers))]
		}
		ops[i] = rebindOp{refID: smallRefIDs[rng.Intn(len(smallRefIDs))], target: target}
	}
	return ops
}

// 在小分支上反复改绑 1000 次，背景分支 20 万节点、100 万条引用。
// 用检查计数证明：初始化后的局部更新工作量不随背景分支规模增长。
func TestLocalUpdatesIndependentOfBackgroundScale(t *testing.T) {
	nodesS, refsS, smallRefIDs, _ := buildGraph(false)
	ops := genOps(smallRefIDs)

	run := func(withBackground bool) (*Tracker, Stats) {
		nodes, refs := nodesS, refsS
		if withBackground {
			nodes, refs, _, _ = buildGraph(true)
		}
		tr, err := NewTracker(nodes, refs, "urn:ds:deprecated:v1")
		if err != nil {
			t.Fatal(err)
		}
		tr.ResetStats() // 只统计初始化之后的局部更新
		for _, op := range ops {
			if _, _, err := tr.Rebind(op.refID, op.target); err != nil {
				t.Fatal(err)
			}
		}
		return tr, tr.Stats()
	}

	trSmall, statsSmall := run(false)
	trFull, statsFull := run(true)

	t.Logf("small-only:   node checks=%d ref checks=%d", statsSmall.NodeChecks, statsSmall.RefChecks)
	t.Logf("with 200k/1M: node checks=%d ref checks=%d", statsFull.NodeChecks, statsFull.RefChecks)

	// 关键证据：有无背景分支，检查计数完全相等。
	if statsSmall != statsFull {
		t.Fatalf("check counts differ: small=%+v full=%+v", statsSmall, statsFull)
	}
	// 绝对上界：远小于一次全图扫描（100 万条引用），更不随背景规模增长。
	if statsFull.NodeChecks > 200000 || statsFull.RefChecks > 1000000 {
		t.Fatalf("local update work too large: %+v", statsFull)
	}

	// 正确性：全量图上的 Impact 与独立全量计算一致。
	_, refsFull, _, bgConsumers := buildGraph(true)
	// 对 refsFull 应用同样的改绑序列。
	byID := map[string]int{}
	for i, r := range refsFull {
		byID[r.ID] = i
	}
	for _, op := range ops {
		refsFull[byID[op.refID]].Target = op.target
	}
	nodesFull, _, _, _ := buildGraph(true)
	want := bruteForce(nodesFull, refsFull, "urn:ds:deprecated:v1")
	if got := trFull.Impact(); !reflect.DeepEqual(got, want) {
		t.Fatalf("final Impact mismatch: got %d consumers, want %d", len(got), len(want))
	}
	if len(bgConsumers) != 100000 {
		t.Fatalf("background consumer count = %d, want %d", len(bgConsumers), 100000)
	}
	// 小分支单独运行的结果应与全量计算一致（同样应用改绑序列）。
	byIDSmall := map[string]int{}
	for i, r := range refsS {
		byIDSmall[r.ID] = i
	}
	for _, op := range ops {
		refsS[byIDSmall[op.refID]].Target = op.target
	}
	wantSmall := bruteForce(nodesS, refsS, "urn:ds:deprecated:v1")
	if got := trSmall.Impact(); !reflect.DeepEqual(got, wantSmall) {
		t.Fatalf("small-only Impact = %v, want %v", got, wantSmall)
	}
}
