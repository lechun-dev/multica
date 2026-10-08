package agent

import "testing"

func TestACPMessageStreamDropsSendAfterClose(t *testing.T) {
	stream := newACPMessageStream(1)
	stream.close()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("send after close panicked: %v", r)
		}
	}()
	stream.send(Message{Type: MessageText, Content: "late"})

	if _, ok := <-stream.ch; ok {
		t.Fatal("message channel should be closed")
	}
}
