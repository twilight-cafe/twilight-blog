package service

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"blog-engine/internal/model"
)

// recordingSender 记录收到的邮件，并让测试能决定投递是否失败。
type recordingSender struct {
	mu       sync.Mutex
	messages []sentMessage
	fail     bool
	done     chan struct{}
}

type sentMessage struct {
	to      string
	subject string
	body    string
}

func (s *recordingSender) Send(to, subject, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, sentMessage{to: to, subject: subject, body: body})
	if s.done != nil {
		close(s.done)
		s.done = nil
	}
	if s.fail {
		return errors.New("resend unavailable")
	}
	return nil
}

func (s *recordingSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

func TestCommentNotifierSkipsWhenNotConfigured(t *testing.T) {
	sender := &recordingSender{}
	notifier := NewCommentNotifier(sender, func() string { return "" })

	notifier.Notify(&model.Comment{ID: 1, TargetType: "article", TargetID: 2, Content: "hi"})
	notifier.Notify(nil)

	// 空收件人表示「关闭通知」，连内容都不该拼。
	time.Sleep(50 * time.Millisecond)
	if sender.count() != 0 {
		t.Fatalf("no mail must be sent when notifications are off, got %d", sender.count())
	}

	// recipient 为 nil（调用方漏传）也不能 panic。
	orphan := NewCommentNotifier(sender, nil)
	orphan.Notify(&model.Comment{ID: 1})
}

func TestCommentNotifierDelivers(t *testing.T) {
	sender := &recordingSender{done: make(chan struct{})}
	notifier := NewCommentNotifier(sender, func() string { return "  owner@example.com  " })

	notifier.Notify(&model.Comment{
		ID:         7,
		AuthorType: "user",
		Nickname:   "访客",
		TargetType: "article",
		TargetID:   3,
		Content:    "写得不错",
	})

	select {
	case <-sender.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the notification was never sent")
	}

	sender.mu.Lock()
	message := sender.messages[0]
	sender.mu.Unlock()

	if message.to != "owner@example.com" {
		t.Fatalf("to = %q", message.to)
	}
	if !strings.Contains(message.subject, "访客") || !strings.Contains(message.subject, "文章") {
		t.Fatalf("subject = %q", message.subject)
	}
	if !strings.Contains(message.body, "写得不错") || !strings.Contains(message.body, "#3") {
		t.Fatalf("body = %q", message.body)
	}
}

func TestCommentNotifierSwallowsSendFailures(t *testing.T) {
	sender := &recordingSender{fail: true, done: make(chan struct{})}
	notifier := NewCommentNotifier(sender, func() string { return "owner@example.com" })

	// 发送失败不能变成 panic 或返回错误：Notify 没有返回值。
	notifier.Notify(&model.Comment{ID: 8, AuthorType: "admin", AdminName: "站长", TargetType: "moment", TargetID: 1})

	select {
	case <-sender.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the notification attempt never happened")
	}
}

func TestCommentNotificationLabels(t *testing.T) {
	for _, test := range []struct {
		targetType string
		want       string
	}{
		{"article", "文章"},
		{"moment", "微语"},
		{"page", "页面"},
		{"weird", "weird"},
	} {
		subject, body := commentNotification(&model.Comment{
			AuthorType: "user",
			Nickname:   "访客",
			TargetType: test.targetType,
			TargetID:   1,
			Content:    "x",
		})
		if !strings.Contains(subject, test.want) || !strings.Contains(body, test.want) {
			t.Fatalf("target %q: subject=%q body=%q", test.targetType, subject, body)
		}
	}

	for _, test := range []struct {
		name    string
		comment model.Comment
		want    string
	}{
		{"admin name", model.Comment{AuthorType: "admin", AdminName: "站长"}, "站长"},
		{"admin without a name", model.Comment{AuthorType: "admin", Nickname: "访客"}, "访客"},
		{"visitor", model.Comment{AuthorType: "user", Nickname: "访客"}, "访客"},
		{"anonymous", model.Comment{AuthorType: "user"}, model.AnonymousDisplayName},
	} {
		comment := test.comment
		comment.TargetType = "article"
		comment.TargetID = 1
		subject, _ := commentNotification(&comment)
		if !strings.Contains(subject, test.want) {
			t.Fatalf("%s: subject = %q, want it to mention %q", test.name, subject, test.want)
		}
	}
}
