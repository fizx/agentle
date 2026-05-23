package trigger

// Kind describes a way executions get created. Adding a trigger kind: append a
// Kind here, handle its dispatch (cron in this package's Scheduler, webhook in
// the API server), and the UI/validation pick it up from Kinds automatically.
type Kind struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Creatable   bool   `json:"creatable"` // can a user create a persistent trigger of this kind?
}

// Kinds is the registry of trigger kinds.
var Kinds = []Kind{
	{Name: "dashboard", Description: "Manual run from the dashboard or API", Creatable: false},
	{Name: "webhook", Description: "Inbound HTTP POST to a generated URL; body becomes event.data", Creatable: true},
	{Name: "cron", Description: "Schedule on a cron expression", Creatable: true},
}

// Creatable reports whether a user may create a persistent trigger of this kind.
func Creatable(kind string) bool {
	for _, k := range Kinds {
		if k.Name == kind {
			return k.Creatable
		}
	}
	return false
}
