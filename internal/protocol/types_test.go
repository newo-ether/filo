package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestNativeDefaultIsDistinctFromUnknown(t *testing.T) {
	for _, source := range []string{`{}`, `{"serviceTier":null}`, `{"serviceTier":"fast"}`} {
		var settings SessionSettings
		if err := json.Unmarshal([]byte(source), &settings); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != source {
			t.Fatalf("presence changed: %s -> %s", source, encoded)
		}
	}
}

func TestPublicMessageAndPageRoundTripPreservesNativeProjection(t *testing.T) {
	const source = `{"messages":[{"id":"native","turnId":"turn","groupId":"turn-assistant","nativeId":"native","textOffset":0,"textContinues":false,"clientId":null,"role":"assistant","timestamp":1.25,"text":"你好🙂\n","imageLinks":["C:/work/two%20words.png"],"activity":{"type":"tool","toolName":"imageView","arguments":"","result":"","state":"succeeded","durationMs":0,"imagePath":"C:/work/two words.png"}}],"nextCursor":null,"queued":[],"runtime":{"status":"idle","activeTurnId":null,"model":null,"contextTokens":null,"contextWindow":null,"serviceTierKnown":true,"serviceTier":null}}`
	var page ConversationPage
	if err := json.Unmarshal([]byte(source), &page); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	var before, after any
	if err := json.Unmarshal([]byte(source), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("native fields changed during Go roundtrip: %s", encoded)
	}
}

func TestNodeCannotSerializeToolPayload(t *testing.T) {
	var node MessageNode
	const source = `{"id":"message","turnId":"turn","role":"assistant","clientId":null,"timestamp":0,"text":"heavy","revision":"revision","textLength":5,"hasContent":true,"activity":{"type":"tool","state":"running","arguments":"heavy","result":"heavy","hasImage":true}}`
	if err := json.Unmarshal([]byte(source), &node); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["text"]; exists {
		t.Fatal("topology leaked a message body")
	}
	var activity map[string]json.RawMessage
	if err := json.Unmarshal(fields["activity"], &activity); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"arguments", "result", "imagePath"} {
		if _, exists := activity[key]; exists {
			t.Fatalf("topology leaked %s", key)
		}
	}
}

func TestCodexServiceInfoWireBytesMatchTSConstant(t *testing.T) {
	encoded, err := json.Marshal(CodexServiceInfo)
	if err != nil {
		t.Fatal(err)
	}
	const expected = `{"protocolVersion":2,"agent":"codex","sessionMode":"existing","messageDelivery":"native-steer","outputMode":"live-messages","supportsStop":true,"supportsApprovals":false,"supportsActivity":true,"supportsSettings":true,"supportsLazyMessages":true}`
	if string(encoded) != expected {
		t.Fatalf("serviceInfo wire bytes changed:\n got %s\nwant %s", encoded, expected)
	}
}

func TestTopologyPageWireShape(t *testing.T) {
	page := TopologyPage{
		Nodes: []MessageNode{{
			MessageIdentity: MessageIdentity{ID: "message", TurnID: "turn", Role: "assistant", ClientID: nil, Timestamp: 0},
			Revision:        "revision", TextLength: 5, HasContent: true,
		}},
		NextCursor: nil,
		Queued:     []QueuedInput{},
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"messages", "pageCursor"} {
		if _, exists := fields[forbidden]; exists {
			t.Fatalf("topology page serialized %q", forbidden)
		}
	}
	for _, required := range []string{"nodes", "nextCursor", "queued"} {
		if _, exists := fields[required]; !exists {
			t.Fatalf("topology page lost required key %q: %s", required, encoded)
		}
	}
	if string(fields["nextCursor"]) != "null" {
		t.Fatalf("nextCursor = %s", fields["nextCursor"])
	}
	// A runtime present on the source page passes through unchanged.
	page.Runtime = &Runtime{Status: "idle"}
	encoded, err = json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["runtime"]; !exists {
		t.Fatalf("runtime dropped: %s", encoded)
	}
}

func TestDefaultTaskRequestTimeoutsMatchTSConstant(t *testing.T) {
	if DefaultTaskRequestTimeouts.Read != 15*time.Second ||
		DefaultTaskRequestTimeouts.ExecutorReady != 90*time.Second ||
		DefaultTaskRequestTimeouts.Create != 60*time.Second ||
		DefaultTaskRequestTimeouts.Mutation != 180*time.Second {
		t.Fatalf("timeouts changed: %+v", DefaultTaskRequestTimeouts)
	}
}
