package tracker

import (
	"reflect"
	"testing"
)

func v(urn string) Node { return Node{URN: urn, Kind: Version} }
func c(urn string) Node { return Node{URN: urn, Kind: Consumer} }

func checkImpact(t *testing.T, tr *Tracker, want []string) {
	t.Helper()
	got := tr.Impact()
	if want == nil {
		want = []string{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Impact() = %v, want %v", got, want)
	}
}

func checkRebind(t *testing.T, tr *Tracker, refID, target string, wantAdded, wantRemoved []string) {
	t.Helper()
	added, removed, err := tr.Rebind(refID, target)
	if err != nil {
		t.Fatalf("Rebind(%s, %q) error: %v", refID, target, err)
	}
	if wantAdded == nil {
		wantAdded = []string{}
	}
	if wantRemoved == nil {
		wantRemoved = []string{}
	}
	if !reflect.DeepEqual(added, wantAdded) || !reflect.DeepEqual(removed, wantRemoved) {
		t.Fatalf("Rebind(%s, %q) = (+%v, -%v), want (+%v, -%v)",
			refID, target, added, removed, wantAdded, wantRemoved)
	}
}

// 场景一：带消费者的环经唯一引用连到废弃版本；停用后整环退出，再启用恢复。
// 另一个从未连通废弃源的环始终不受影响。
func TestRingDetachAndReattach(t *testing.T) {
	nodes := []Node{
		v("urn:ds:lib:v1"),                                      // 废弃版本
		v("urn:ds:r1:v1"), v("urn:ds:r2:v1"), v("urn:ds:r3:v1"), // 环 A
		c("urn:consumer:a"), c("urn:consumer:b"), c("urn:consumer:c"),
		v("urn:ds:q1:v1"), v("urn:ds:q2:v1"), // 环 B（永不连通）
		c("urn:consumer:q"),
	}
	refs := []Ref{
		{ID: "r12", Owner: "urn:ds:r1:v1", Target: "urn:ds:r2:v1"},
		{ID: "r23", Owner: "urn:ds:r2:v1", Target: "urn:ds:r3:v1"},
		{ID: "r31", Owner: "urn:ds:r3:v1", Target: "urn:ds:r1:v1"},
		{ID: "link", Owner: "urn:ds:r1:v1", Target: "urn:ds:lib:v1"}, // 唯一连接
		{ID: "ca", Owner: "urn:consumer:a", Target: "urn:ds:r1:v1"},
		{ID: "cb", Owner: "urn:consumer:b", Target: "urn:ds:r2:v1"},
		{ID: "cc", Owner: "urn:consumer:c", Target: "urn:ds:r3:v1"},
		{ID: "q12", Owner: "urn:ds:q1:v1", Target: "urn:ds:q2:v1"},
		{ID: "q21", Owner: "urn:ds:q2:v1", Target: "urn:ds:q1:v1"},
		{ID: "cq", Owner: "urn:consumer:q", Target: "urn:ds:q1:v1"},
	}
	tr, err := NewTracker(nodes, refs, "urn:ds:lib:v1")
	if err != nil {
		t.Fatal(err)
	}
	ring := []string{"urn:consumer:a", "urn:consumer:b", "urn:consumer:c"}
	checkImpact(t, tr, ring)

	// 停用唯一连接：整环退出影响范围。
	checkRebind(t, tr, "link", "", nil, ring)
	checkImpact(t, tr, nil)

	// 重新启用：整环恢复。
	checkRebind(t, tr, "link", "urn:ds:lib:v1", ring, nil)
	checkImpact(t, tr, ring)

	// 再次停用并启用，确认可重复。
	checkRebind(t, tr, "link", "", nil, ring)
	checkRebind(t, tr, "link", "urn:ds:lib:v1", ring, nil)
	checkImpact(t, tr, ring)
}

// 场景二：菱形替代路径、重复引用、消费者绑定多个版本，
// 以及同一数据集的两个版本（只有真正引用废弃版本的一侧受影响）。
func TestDiamondDuplicatesAndMultiBinding(t *testing.T) {
	nodes := []Node{
		v("urn:ds:lib:v1"), // 废弃版本
		v("urn:ds:lib:v2"), // 同数据集另一版本，不连通废弃源
		v("urn:ds:s:v1"),
		v("urn:ds:a:v1"), v("urn:ds:b:v1"),
		v("urn:ds:data:v1"), v("urn:ds:data:v2"), // 同一数据集的两个版本
		c("urn:consumer:dia"), c("urn:consumer:multi"), c("urn:consumer:data"),
	}
	refs := []Ref{
		{ID: "sa1", Owner: "urn:ds:s:v1", Target: "urn:ds:a:v1"},
		{ID: "sa2", Owner: "urn:ds:s:v1", Target: "urn:ds:a:v1"}, // 重复引用
		{ID: "sb", Owner: "urn:ds:s:v1", Target: "urn:ds:b:v1"},
		{ID: "adep", Owner: "urn:ds:a:v1", Target: "urn:ds:lib:v1"},
		{ID: "bdep", Owner: "urn:ds:b:v1", Target: "urn:ds:lib:v1"},
		{ID: "cdia", Owner: "urn:consumer:dia", Target: "urn:ds:s:v1"},
		{ID: "cma", Owner: "urn:consumer:multi", Target: "urn:ds:a:v1"},
		{ID: "cmb", Owner: "urn:consumer:multi", Target: "urn:ds:b:v1"},
		{ID: "d1dep", Owner: "urn:ds:data:v1", Target: "urn:ds:lib:v1"},
		{ID: "d2other", Owner: "urn:ds:data:v2", Target: "urn:ds:lib:v2"},
		{ID: "cdata", Owner: "urn:consumer:data", Target: "urn:ds:data:v2"},
	}
	tr, err := NewTracker(nodes, refs, "urn:ds:lib:v1")
	if err != nil {
		t.Fatal(err)
	}
	dia := []string{"urn:consumer:dia"}
	multi := []string{"urn:consumer:multi"}
	both := []string{"urn:consumer:dia", "urn:consumer:multi"}
	checkImpact(t, tr, both)

	// 重复引用停用一条：无变化。
	checkRebind(t, tr, "sa1", "", nil, nil)
	checkImpact(t, tr, both)
	// 重复引用全部停用：s 仍经 b 连通，无变化。
	checkRebind(t, tr, "sa2", "", nil, nil)
	checkImpact(t, tr, both)
	// 菱形另一侧也断开：s 失联，只有 dia 退出（multi 直连 a、b）。
	checkRebind(t, tr, "sb", "", nil, dia)
	checkImpact(t, tr, multi)
	// a 侧断开：multi 仍有 b 侧路径，无变化。
	checkRebind(t, tr, "adep", "", nil, nil)
	checkImpact(t, tr, multi)
	// b 侧也断开：multi 退出。
	checkRebind(t, tr, "bdep", "", nil, multi)
	checkImpact(t, tr, nil)
	// data 消费者改绑到真正引用废弃版本的同数据集版本：进入影响范围。
	checkRebind(t, tr, "cdata", "urn:ds:data:v1", []string{"urn:consumer:data"}, nil)
	checkImpact(t, tr, []string{"urn:consumer:data"})
	// 改回未连通的一侧：退出影响范围。
	checkRebind(t, tr, "cdata", "urn:ds:data:v2", nil, []string{"urn:consumer:data"})
	checkImpact(t, tr, nil)
	// 恢复 a 侧：multi 经 a 恢复；dia 仍因 s 失联而不受影响。
	checkRebind(t, tr, "adep", "urn:ds:lib:v1", multi, nil)
	checkImpact(t, tr, multi)
	// 恢复 s->b 但 b 仍失联：无变化。
	checkRebind(t, tr, "sb", "urn:ds:b:v1", nil, nil)
	checkImpact(t, tr, multi)
	// 恢复 b 侧：s 重新连通，dia 恢复。
	checkRebind(t, tr, "bdep", "urn:ds:lib:v1", dia, nil)
	checkImpact(t, tr, both)
}

// 同一 Rebind 中先删后增导致的“暂时消失又恢复”不得报告为变化。
func TestTransientRestoreNotReported(t *testing.T) {
	nodes := []Node{
		v("urn:ds:lib:v1"), v("urn:ds:lib:v2"),
		v("urn:ds:x:v1"),
		c("urn:consumer:k"),
	}
	refs := []Ref{
		{ID: "xdep", Owner: "urn:ds:x:v1", Target: "urn:ds:lib:v1"},
		{ID: "ck", Owner: "urn:consumer:k", Target: "urn:ds:x:v1"},
	}
	tr, err := NewTracker(nodes, refs, "urn:ds:lib:v1")
	if err != nil {
		t.Fatal(err)
	}
	checkImpact(t, tr, []string{"urn:consumer:k"})
	// x 的引用从 lib:v1 改绑到 lib:v2（也连通废弃源？不——lib:v2 不连通）。
	// 先构造 lib:v2 -> lib:v1，使改绑后 x 仍连通。
	nodes2 := []Node{v("urn:ds:lib:v1"), v("urn:ds:lib:v2"), v("urn:ds:x:v1"), c("urn:consumer:k")}
	refs2 := []Ref{
		{ID: "xdep", Owner: "urn:ds:x:v1", Target: "urn:ds:lib:v1"},
		{ID: "v2dep", Owner: "urn:ds:lib:v2", Target: "urn:ds:lib:v1"},
		{ID: "ck", Owner: "urn:consumer:k", Target: "urn:ds:x:v1"},
	}
	tr2, err := NewTracker(nodes2, refs2, "urn:ds:lib:v1")
	if err != nil {
		t.Fatal(err)
	}
	// x 改绑到 lib:v2：中途 x 短暂失联又经 v2 恢复，不得报告变化。
	checkRebind(t, tr2, "xdep", "urn:ds:lib:v2", nil, nil)
	checkImpact(t, tr2, []string{"urn:consumer:k"})
	// 改绑到空：真正退出。
	checkRebind(t, tr2, "xdep", "", nil, []string{"urn:consumer:k"})
	checkImpact(t, tr2, nil)
}
