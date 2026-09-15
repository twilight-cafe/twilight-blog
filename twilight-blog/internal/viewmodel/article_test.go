package viewmodel

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestUpdateRequestsDistinguishAnOmittedBodyFromAnEmptyOne 钉住「省略正文」与
// 「正文传空串」是两件不同的事——这正是 R3-15 的修复点。
//
// 为什么它关乎安全：Content 曾经是 string，于是任何只带 title 的 PUT
// （改标题、改标签、前端只提交改动字段）都会把正文替换成空串，返回 200，
// 日志里没有任何异常。数据丢了，而且看起来像一次成功。
//
// 断言刻意用**反序列化后的指针是不是 nil** 来表达，而不是看 Content 的值：
// 只有指针能区分这两种输入，值域上它们长得一模一样（都是 ""）。
//
// 反证方式：把 Content 的类型改回 string → 第一个用例拿到的是零值 ""，
// 与「显式传空串」不可区分，本条按 nil 判定失败。
func TestUpdateRequestsDistinguishAnOmittedBodyFromAnEmptyOne(t *testing.T) {
	t.Run("article", func(t *testing.T) {
		var omitted UpdateArticleRequest
		if err := json.Unmarshal([]byte(`{"title":"only a title"}`), &omitted); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if omitted.Content != nil {
			t.Fatalf("an omitted content must stay nil (got %q); otherwise the update clears the body", *omitted.Content)
		}
		if err := omitted.Validate(); err != nil {
			t.Fatalf("a title-only update must still be valid: %v", err)
		}

		var explicit UpdatePageRequest
		if err := json.Unmarshal([]byte(`{"title":"t","content":""}`), &explicit); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if explicit.Content == nil {
			t.Fatal(`an explicit "" must be distinguishable from a missing field`)
		}
		if *explicit.Content != "" {
			t.Fatalf(`explicit content = %q, want ""`, *explicit.Content)
		}
	})

	t.Run("page", func(t *testing.T) {
		var omitted UpdatePageRequest
		if err := json.Unmarshal([]byte(`{"title":"only a title"}`), &omitted); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if omitted.Content != nil {
			t.Fatalf("an omitted content must stay nil (got %q); otherwise the update clears the body", *omitted.Content)
		}
		if err := omitted.Validate(); err != nil {
			t.Fatalf("a title-only update must still be valid: %v", err)
		}
	})
}

// TestBatchDeleteRequestEnforcesTheIDCap 钉住批量操作的 id 数量上限。
//
// 为什么它是一条安全测试而不是格式校验：批量删除会为每个 id 做一次索引侧处理，
// 而请求体上限是 50MB（main.go 的 BodyLimit）。没有数量上限时，一个几 MB 的请求
// 就能塞进上百万个 id，在服务端变成以小时计的串行外呼——成本由**请求里的 id 个数**
// 决定，而不是实际影响的行数（不存在的 id 同样产生开销，实测如此）。
//
// 反证方式：把 Validate 里的 MaxBatchIDs 判断删掉 → 超限用例会返回 nil，本条失败。
func TestBatchDeleteRequestEnforcesTheIDCap(t *testing.T) {
	atCap := make([]int64, MaxBatchIDs)
	for i := range atCap {
		atCap[i] = int64(i + 1)
	}
	if err := (&BatchDeleteRequest{IDs: atCap}).Validate(); err != nil {
		t.Fatalf("exactly %d ids must be accepted, got %v", MaxBatchIDs, err)
	}

	overCap := make([]int64, MaxBatchIDs+1)
	for i := range overCap {
		overCap[i] = int64(i + 1)
	}
	err := (&BatchDeleteRequest{IDs: overCap}).Validate()
	if err == nil {
		t.Fatalf("%d ids must be rejected", len(overCap))
	}
	if !strings.Contains(err.Error(), "at most") {
		t.Fatalf("error %q should tell the caller what the limit is", err.Error())
	}

	// 上限判断必须排在逐项校验之前：否则一个上百万项的请求会先被完整遍历一遍，
	// 而这正是要避免的开销。
	huge := make([]int64, 5_000_000)
	for i := range huge {
		huge[i] = int64(i + 1)
	}
	if err := (&BatchDeleteRequest{IDs: huge}).Validate(); err == nil {
		t.Fatal("a multi-million entry request must be rejected")
	}

	// 既有行为不能被这次改动破坏。
	if err := (&BatchDeleteRequest{}).Validate(); err == nil {
		t.Fatal("an empty ids array must still be rejected")
	}
	if err := (&BatchDeleteRequest{IDs: []int64{1, 0, 3}}).Validate(); err == nil {
		t.Fatal("a non-positive id must still be rejected")
	}
}
