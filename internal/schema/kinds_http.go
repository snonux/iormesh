package schema

// KindHTTP is one HTTP request as the Java agent's HTTP hooks recorded it
// (M3 step 7, decision #22): not a syscall but an event of its own,
// stamped when the request completed (the client's response returned,
// the server's exchange closed), on the thread and stack that made it.
// Event.HTTP describes the request; conn_id names the connection that
// carried it when the joiner found it; fd is NoFD — the agent records the
// socket's addresses, not its descriptor.
const KindHTTP = "http"

// validateHTTP keeps the http object and its kind together: an http event
// without its request says nothing, and a request on another kind would
// be counted twice by "requests per connection".
func (e *Event) validateHTTP() error {
	if (e.Event == KindHTTP) != (e.HTTP != nil) {
		return invalidf("http object on a %s event (set exactly on http events)", e.Event)
	}
	if e.HTTP != nil && e.HTTP.Method == "" {
		return invalidf("http event without a method")
	}
	return nil
}
