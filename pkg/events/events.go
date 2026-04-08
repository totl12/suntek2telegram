package events

// ImageEvent carries image data received from a camera trap.
type ImageEvent struct {
	TrapID   int64
	TrapName string
	ChatID   int64
	Data     []byte
}
