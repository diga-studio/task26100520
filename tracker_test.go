package lineage

import (
	"reflect"
	"testing"
)

// mustTracker 构造 Tracker，失败即终止测试。
func mustTracker(t *testing.T, nodes []Node, refs []Ref, deprecated string) *Tracker {
	t.Helper()
	tr, err := NewTracker(nodes, refs, deprecated)
	if err != nil {
		t.Fatalf("NewTracker: %v", err)
	}
	return tr
}

// mustRebind 执行 Rebind 并对结果排序后返回。
func mustRebind(t *testing.T, tr *Tracker, refID, target string) (added, removed []string) {
	t.Helper()
	added, removed, err := tr.Rebind(refID, target)
	if err != nil {
		t.Fatalf("Rebind(%s -> %q): %v", refID, target, err)
	}
	return added, removed
}

func checkImpact(t *testing.T, tr *Tracker, want []string) {
	t.Helper()
	got := tr.Impact()
	if got == nil {
		got = []string{}
	}
	if want == nil {
		want = []string{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Impact() = %v, want %v", got, want)
	}
}

func checkDiff(t *testing.T, added, removed, wantAdded, wantRemoved []string) {
	t.Helper()
	norm := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	added, removed = norm(added), norm(removed)
	wantAdded, wantRemoved = norm(wantAdded), norm(wantRemoved)
	if !reflect.DeepEqual(added, wantAdded) || !reflect.DeepEqual(removed, wantRemoved) {
		t.Fatalf("diff = (+%v, -%v), want (+%v, -%v)", added, removed, wantAdded, wantRemoved)
	}
}

// 场景 1：带消费者的环通过唯一引用连到废弃版本。
// 停用该引用后整环退出影响范围，再启用时恢复；
// 另一个从未连通过废弃源的环始终不受影响。
func TestCycleGateRef(t *testing.T) {
	nodes := []Node{
		{URN: "urn:ds:dep:1", Kind: Version},
		// 环 A：vA1 -> vA2 -> vA1，消费者 cA 绑在 vA1 上
		{URN: "urn:ds:a:1", Kind: Version},
		{URN: "urn:ds:a:2", Kind: Version},
		{URN: "urn:consumer:a", Kind: Consumer},
		// 环 B：自成一体，从不连通废弃源
		{URN: "urn:ds:b:1", Kind: Version},
		{URN: "urn:ds:b:2", Kind: Version},
		{URN: "urn:consumer:b", Kind: Consumer},
	}
	refs := []Ref{
		{ID: "rA1", Owner: "urn:ds:a:1", Target: "urn:ds:a:2"},
		{ID: "rA2", Owner: "urn:ds:a:2", Target: "urn:ds:a:1"},
		{ID: "gate", Owner: "urn:ds:a:1", Target: "urn:ds:dep:1"}, // 唯一出口
		{ID: "cA", Owner: "urn:consumer:a", Target: "urn:ds:a:1"},
		{ID: "rB1", Owner: "urn:ds:b:1", Target: "urn:ds:b:2"},
		{ID: "rB2", Owner: "urn:ds:b:2", Target: "urn:ds:b:1"},
		{ID: "cB", Owner: "urn:consumer:b", Target: "urn:ds:b:1"},
	}
	tr := mustTracker(t, nodes, refs, "urn:ds:dep:1")

	// 初始：环 A 经 gate 可达废弃版本，cA 受影响；环 B 不连通，cB 不受影响。
	checkImpact(t, tr, []string{"urn:consumer:a"})

	// 停用唯一出口：整环退出影响范围，cA 退出；cB 仍不受影响。
	added, removed := mustRebind(t, tr, "gate", "")
	checkDiff(t, added, removed, nil, []string{"urn:consumer:a"})
	checkImpact(t, tr, nil)

	// 环内改绑（不改变连通性）：无任何变化。
	added, removed = mustRebind(t, tr, "rA1", "urn:ds:a:1") // 自引用
	checkDiff(t, added, removed, nil, nil)
	checkImpact(t, tr, nil)

	// 重新启用 gate：整环恢复影响。
	added, removed = mustRebind(t, tr, "gate", "urn:ds:dep:1")
	checkDiff(t, added, removed, []string{"urn:consumer:a"}, nil)
	checkImpact(t, tr, []string{"urn:consumer:a"})

	// 环 B 内部改绑：始终不受影响。
	added, removed = mustRebind(t, tr, "rB1", "")
	checkDiff(t, added, removed, nil, nil)
	added, removed = mustRebind(t, tr, "rB1", "urn:ds:b:2")
	checkDiff(t, added, removed, nil, nil)
	checkImpact(t, tr, []string{"urn:consumer:a"})
}

// 场景 2：菱形替代路径 + 重复引用 + 消费者绑定多个版本，
// 逐次改绑核对准确差集；同一数据集的两个版本只影响真正引用废弃版本的一侧。
func TestDiamondDuplicateMultiBind(t *testing.T) {
	nodes := []Node{
		{URN: "urn:ds:dep:1", Kind: Version},
		{URN: "urn:ds:dep:2", Kind: Version}, // 同一数据集的另一版本，不废弃
		// 菱形：top -> left/right -> dep:1
		{URN: "urn:ds:top:1", Kind: Version},
		{URN: "urn:ds:left:1", Kind: Version},
		{URN: "urn:ds:right:1", Kind: Version},
		{URN: "urn:consumer:multi", Kind: Consumer}, // 同时绑 top 与 dep:2
		{URN: "urn:consumer:clean", Kind: Consumer}, // 只绑 dep:2，永不受影响
	}
	refs := []Ref{
		{ID: "top-left", Owner: "urn:ds:top:1", Target: "urn:ds:left:1"},
		{ID: "top-right", Owner: "urn:ds:top:1", Target: "urn:ds:right:1"},
		{ID: "left-dep", Owner: "urn:ds:left:1", Target: "urn:ds:dep:1"},
		{ID: "right-dep", Owner: "urn:ds:right:1", Target: "urn:ds:dep:1"},
		// 重复引用：与 left-dep 同 owner 同 target
		{ID: "left-dep-dup", Owner: "urn:ds:left:1", Target: "urn:ds:dep:1"},
		// 消费者多绑
		{ID: "c-multi-top", Owner: "urn:consumer:multi", Target: "urn:ds:top:1"},
		{ID: "c-multi-dep2", Owner: "urn:consumer:multi", Target: "urn:ds:dep:2"},
		{ID: "c-clean-dep2", Owner: "urn:consumer:clean", Target: "urn:ds:dep:2"},
	}
	tr := mustTracker(t, nodes, refs, "urn:ds:dep:1")
	checkImpact(t, tr, []string{"urn:consumer:multi"})

	// 断 left-dep：还有重复引用 left-dep-dup，无变化。
	added, removed := mustRebind(t, tr, "left-dep", "")
	checkDiff(t, added, removed, nil, nil)
	checkImpact(t, tr, []string{"urn:consumer:multi"})

	// 再断重复引用：left 支路断开，但 right 支路仍在，无变化。
	added, removed = mustRebind(t, tr, "left-dep-dup", "")
	checkDiff(t, added, removed, nil, nil)
	checkImpact(t, tr, []string{"urn:consumer:multi"})

	// 断 right-dep：菱形两条路全断，top 失去路径，multi 退出。
	added, removed = mustRebind(t, tr, "right-dep", "")
	checkDiff(t, added, removed, nil, []string{"urn:consumer:multi"})
	checkImpact(t, tr, nil)

	// 把 left-dep 改绑到 dep:2（非废弃版本）：不恢复影响。
	added, removed = mustRebind(t, tr, "left-dep", "urn:ds:dep:2")
	checkDiff(t, added, removed, nil, nil)
	checkImpact(t, tr, nil)

	// 恢复 right-dep：right -> dep:1 通路恢复，multi 重新受影响。
	added, removed = mustRebind(t, tr, "right-dep", "urn:ds:dep:1")
	checkDiff(t, added, removed, []string{"urn:consumer:multi"}, nil)
	checkImpact(t, tr, []string{"urn:consumer:multi"})

	// 把 multi 对 top 的绑定改到 dep:2：multi 只剩 dep:2，退出影响。
	added, removed = mustRebind(t, tr, "c-multi-top", "urn:ds:dep:2")
	checkDiff(t, added, removed, nil, []string{"urn:consumer:multi"})
	checkImpact(t, tr, nil)

	// clean 自始至终只引用 dep:2，验证“同一数据集不同版本不合并”：
	// 废弃的是 dep:1，引用 dep:2 的一侧从未受影响。
	checkImpact(t, tr, nil)
}
