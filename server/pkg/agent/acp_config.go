package agent

// 2026-10-08 coder(lq): ACP select options may be flat or grouped by provider; values remain opaque in either form.
type acpSelectChoice struct {
	Value string `json:"value"`
	Name  string `json:"name"`
}

type acpSelectEntry struct {
	Value   string            `json:"value"`
	Name    string            `json:"name"`
	Options []acpSelectChoice `json:"options"`
}

func flattenACPSelectChoices(entries []acpSelectEntry) []acpSelectChoice {
	choices := make([]acpSelectChoice, 0, len(entries))
	for _, entry := range entries {
		if entry.Value != "" {
			choices = append(choices, acpSelectChoice{Value: entry.Value, Name: entry.Name})
		} else {
			choices = append(choices, entry.Options...)
		}
	}
	return choices
}
