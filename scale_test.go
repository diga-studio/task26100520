package lineage

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// 小规模分支的参数：节点与引用数有界，改绑只发生在这里。
const (
	sbVersions  = 25
	sbConsumers = 5
	sbRefs      = 100
	sbRebinds   = 1000
)

// buildSmallBranch 用固定种子生成小分支的节点与引用（不依赖背景规模）。
// 小分支只引用自身版本或共享的废弃源。
func buildSmallBranch() (nodes []Node, refs []Ref) {
	rng := rand.New(rand.NewSource(42))
	dep := "urn:dep"
	var versions []string
	for i := 0; i < sbVersions; i++ {
		urn := fmt.Sprintf("urn:sb:v:%d", i)
		nodes = append(nodes, Node{URN: urn, Kind: Version})
		versions = append(versions, urn)
	}
	for i := 0; i < sbConsumers; i++ {
		nodes = append(nodes, Node{URN: fmt.Sprintf("urn:sb:c:%d", i), Kind: Consumer})
	}
	pickTarget := func() string {
		switch r := rng.Intn(20); {
		case r < 14:
			return versions[rng.Intn(len(versions))]
		case r < 17:
			return dep
		default:
			return ""
		}
	}
	for i := 0; i < sbRefs; i++ {
		owner := nodes[rng.Intn(len(nodes))].URN
		refs = append(refs, Ref{ID: fmt.Sprintf("sb:r:%d", i), Owner: owner, Target: pickTarget()})
	}
	return nodes, refs
}

// rebindScript 用固定种子生成 1000 次改绑操作（不依赖背景规模）。
func rebindScript(sbRefCount int) [][2]string {
	rng := rand.New(rand.NewSource(7))
	var versions []string
	for i := 0; i < sbVersions; i++ {
		versions = append(versions, fmt.Sprintf("urn:sb:v:%d", i))
	}
	script := make([][2]string, 0, sbRebinds)
	for i := 0; i < sbRebinds; i++ {
		refID := fmt.Sprintf("sb:r:%d", rng.Intn(sbRefCount))
		var target string
		switch r := rng.Intn(20); {
		case r < 12:
			target = versions[rng.Intn(len(versions))]
		case r < 16:
			target = "urn:dep"
		default:
			target = ""
		}
		script = append(script, [2]string{refID, target})
	}
	return script
}

// buildBackground 生成背景分支：一条长链保证全部连通废弃源，
// 再加随机引用与一批消费者。背景只引用自身或废弃源。
func buildBackground(nNodes, nRefs int) (nodes []Node, refs []Ref, consumers []string) {
	rng := rand.New(rand.NewSource(99))
	dep := "urn:dep"
	nVersions := nNodes - 1000 // 1000 个背景消费者
	versions := make([]string, 0, nVersions)
	for i := 0; i < nVersions; i++ {
		urn := fmt.Sprintf("urn:bg:v:%d", i)
		nodes = append(nodes, Node{URN: urn, Kind: Version})
		versions = append(versions, urn)
	}
	for i := 0; i < 1000; i++ {
		urn := fmt.Sprintf("urn:bg:c:%d", i)
		nodes = append(nodes, Node{URN: urn, Kind: Consumer})
		consumers = append(consumers, urn)
	}
	// 主链：bg:0 -> dep，bg:i+1 -> bg:i，保证全部可达废弃源。
	refs = append(refs, Ref{ID: "bg:r:0", Owner: versions[0], Target: dep})
	for i := 1; i < nVersions && len(refs) < nRefs; i++ {
		refs = append(refs, Ref{ID: fmt.Sprintf("bg:r:%d", len(refs)), Owner: versions[i], Target: versions[i-1]})
	}
	// 随机补充引用（target 取更小下标，保持 DAG）。
	for len(refs) < nRefs {
		o := 1 + rng.Intn(nVersions-1)
		tg := rng.Intn(o)
		refs = append(refs, Ref{
			ID: fmt.Sprintf("bg:r:%d", len(refs)), Owner: versions[o], Target: versions[tg]})
	}
	// 背景消费者各绑一个随机背景版本。
	for i, c := range consumers {
		refs = append(refs, Ref{
			ID: fmt.Sprintf("bg:cr:%d", i), Owner: c, Target: versions[rng.Intn(nVersions)]})
	}
	return nodes, refs, consumers
}

// runLocality 在给定背景规模下执行完整流程，返回最终 Impact 与检查计数。
func runLocality(t *testing.T, bgNodes, bgRefs int) ([]string, Stats) {
	t.Helper()
	dep := "urn:dep"
	sbNodes, sbRefs := buildSmallBranch()
	bgNodesList, bgRefsList, bgConsumers := buildBackground(bgNodes, bgRefs)

	nodes := append([]Node{{URN: dep, Kind: Version}}, sbNodes...)
	nodes = append(nodes, bgNodesList...)
	refs := append(append([]Ref{}, sbRefs...), bgRefsList...)

	tr, err := NewTracker(nodes, refs, dep)
	if err != nil {
		t.Fatalf("NewTracker: %v", err)
	}
	tr.ResetStats() // 只统计初始化之后的局部更新

	// 小分支 oracle 的节点与引用视图（含共享废弃源）。
	oracleNodes := append([]Node{{URN: dep, Kind: Version}}, sbNodes...)
	oracleRefs := append([]Ref{}, sbRefs...)

	script := rebindScript(len(sbRefs))
	prev := bruteImpact(oracleNodes, oracleRefs, dep)
	for i, op := range script {
		added, removed, err := tr.Rebind(op[0], op[1])
		if err != nil {
			t.Fatalf("step %d: Rebind: %v", i, err)
		}
		// 同步 oracle 并核对差集。
		for j := range oracleRefs {
			if oracleRefs[j].ID == op[0] {
				oracleRefs[j].Target = op[1]
			}
		}
		after := bruteImpact(oracleNodes, oracleRefs, dep)
		wantAdded, wantRemoved := diffSets(prev, after)
		if !equalStrSet(added, wantAdded) || !equalStrSet(removed, wantRemoved) {
			t.Fatalf("step %d: diff = (+%v, -%v), oracle = (+%v, -%v)",
				i, added, removed, wantAdded, wantRemoved)
		}
		prev = after
	}

	// 最终 Impact = 背景消费者（恒受影响）+ 小分支 oracle 结果。
	want := append([]string{}, bgConsumers...)
	want = append(want, prev...)
	sort.Strings(want)
	got := tr.Impact()
	if !equalStrSet(got, want) {
		t.Fatalf("final Impact mismatch: got %d consumers, want %d", len(got), len(want))
	}
	return got, tr.Stats()
}

// 局部性证明：同样的 1000 次小分支改绑，在两种背景规模下
// 检查计数完全一致，且绝对值远小于背景分支规模。
func TestLocalityIndependentOfBackground(t *testing.T) {
	_, stats1 := runLocality(t, 20_000, 100_000)
	impact2, stats2 := runLocality(t, 200_000, 1_000_000)

	t.Logf("背景 2万节点/10万引用: 节点检查 %d 次, 引用检查 %d 次",
		stats1.NodesVisited, stats1.EdgesScanned)
	t.Logf("背景 20万节点/100万引用: 节点检查 %d 次, 引用检查 %d 次",
		stats2.NodesVisited, stats2.EdgesScanned)
	t.Logf("最终受影响消费者数: %d", len(impact2))

	if stats1 != stats2 {
		t.Fatalf("检查计数随背景规模变化: %+v vs %+v", stats1, stats2)
	}
	// 绝对上界：每次改绑的节点检查不超过小分支规模的常数倍，
	// 引用检查不超过小分支引用数的常数倍；与背景规模（百万级）无关。
	maxNodes := int64(sbRebinds * (sbVersions + sbConsumers) * 4)
	maxEdges := int64(sbRebinds * sbRefs * 4)
	if stats2.NodesVisited > maxNodes {
		t.Fatalf("节点检查次数 %d 超过局部上界 %d", stats2.NodesVisited, maxNodes)
	}
	if stats2.EdgesScanned > maxEdges {
		t.Fatalf("引用检查次数 %d 超过局部上界 %d", stats2.EdgesScanned, maxEdges)
	}
}
