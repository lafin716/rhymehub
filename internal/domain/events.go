package domain

// EventType is the task_events.type discriminator. Payload shapes are documented in docs/DATA_MODEL.md.
type EventType string

const (
	EventStateChanged       EventType = "state_changed"       // {from,to,reason}
	EventUserMessage        EventType = "user_message"        // {text,author}
	EventAgentText          EventType = "agent_text"          // {text}
	EventToolUse            EventType = "tool_use"            // {name,input}
	EventToolResult         EventType = "tool_result"         // {ok,output}
	EventAgentError         EventType = "agent_error"         // {message}
	EventUsage              EventType = "usage"               // {cost_usd,input_tokens,output_tokens,turns}
	EventQuestion           EventType = "question"            // {text}
	EventAnswer             EventType = "answer"              // {text,author}
	EventPermissionRequest  EventType = "permission_request"  // {denials:[{tool,input}]}
	EventPermissionDecision EventType = "permission_decision" // {allow,tools,author}
	EventTestResult         EventType = "test_result"         // {status,exit_code,output_tail,duration_ms}
	EventDiff               EventType = "diff"                // {stat,files}
	EventMerge              EventType = "merge"               // {commit,branch}
	EventRelease            EventType = "release"             // {release_id,version,status}
	EventLog                EventType = "log"                 // {level,message}
)

// Valid reports whether t is a known event type.
func (t EventType) Valid() bool {
	switch t {
	case EventStateChanged, EventUserMessage, EventAgentText, EventToolUse, EventToolResult,
		EventAgentError, EventUsage, EventQuestion, EventAnswer, EventPermissionRequest,
		EventPermissionDecision, EventTestResult, EventDiff, EventMerge, EventRelease, EventLog:
		return true
	}
	return false
}
