package conform

type Class string

const (
	ClassJSON       Class = "json"
	ClassCrashGo    Class = "crash:go"
	ClassCrashPy    Class = "crash:python"
	ClassCrashJVM   Class = "crash:jvm"
	ClassNginxError Class = "nginx-error:error"
	ClassNginxFatal Class = "nginx-error:fatal"
	ClassOther      Class = "other"
)

type Violation struct {
	Line      int    `json:"line,omitempty"`
	Project   string `json:"project,omitempty"`
	Container string `json:"container,omitempty"`
	Section   string `json:"section"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
}

type Options struct {
	ExpectVersion string
	ExpectService string
	Format        string
}
