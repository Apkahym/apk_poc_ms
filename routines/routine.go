package routines

type Routine interface {
	Start() error
	Stop() error
	Restart() error
	Reload() error
	Message(body any) error
	Process(handler func(), body any) error
}
