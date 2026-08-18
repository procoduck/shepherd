package visual

import (
	"fmt"
)

// DiffClass identifies the category of an upgrade-check finding.
type DiffClass string

const (
	// DiffComponentRemoved means the component no longer exists in the new schema.
	DiffComponentRemoved DiffClass = "component_removed"
	// DiffAttrRemoved means a prop set on the node no longer exists as an attribute.
	DiffAttrRemoved DiffClass = "attr_removed"
	// DiffAttrAddedRequired means a required attribute was added that the node lacks.
	DiffAttrAddedRequired DiffClass = "attr_added_required"
	// DiffEnumValueRemoved means the node's current enum value is no longer valid.
	DiffEnumValueRemoved DiffClass = "enum_value_removed"
	// DiffPortTypeChanged means an input or output port's wire type changed.
	DiffPortTypeChanged DiffClass = "port_type_changed"
	// DiffStabilityChanged means the component's stability level changed.
	DiffStabilityChanged DiffClass = "stability_changed"
	// DiffMigrationAvailable means the overlay supplies a rename migration for this component.
	DiffMigrationAvailable DiffClass = "migration_available"
)

// UpgradeItem is a single finding from an upgrade-check diff.
type UpgradeItem struct {
	NodeID    string    `json:"node_id"`
	NodeLabel string    `json:"node_label"`
	Component string    `json:"component"`
	Class     DiffClass `json:"class"`
	Detail    string    `json:"detail,omitempty"`
}

// UpgradeCheckResponse is the response from POST /visual/upgrade-check.
type UpgradeCheckResponse struct {
	OldVersion   string        `json:"old_version"`
	NewVersion   string        `json:"new_version"`
	Items        []UpgradeItem `json:"items"`
	NeedsUpgrade bool          `json:"needs_upgrade"`
}

// UpgradeSchemaPayload is the full schema payload used by upgrade-check.
// It includes fields (stability, required, values) not needed by the renderer.
type UpgradeSchemaPayload struct {
	Components map[string]UpgradeComponentSchema `json:"components"`
	Migrations map[string]MigrationEntry         `json:"migrations,omitempty"`
}

// UpgradeComponentSchema is a component definition in the upgrade schema.
type UpgradeComponentSchema struct {
	Stability  string              `json:"stability"`
	Attributes []UpgradeAttrSchema `json:"attributes"`
	Blocks     []UpgradeBlockDef   `json:"blocks,omitempty"`
	Inputs     []UpgradePortSchema `json:"inputs"`
	Outputs    []UpgradePortSchema `json:"outputs"`
}

// UpgradeBlockDef describes a nested block in the upgrade schema.
type UpgradeBlockDef struct {
	Name string `json:"name"`
}

// UpgradeAttrSchema describes a single attribute in the upgrade schema.
type UpgradeAttrSchema struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Values   []string `json:"values,omitempty"`
}

// UpgradePortSchema describes a port (input or output) in the upgrade schema.
type UpgradePortSchema struct {
	Type        string `json:"type"`
	Cardinality string `json:"cardinality"`
	Export      string `json:"export,omitempty"`
	Prop        string `json:"prop,omitempty"`
}

// MigrationEntry describes a rename migration from the overlay.
type MigrationEntry struct {
	To          string            `json:"to"`
	PropRenames map[string]string `json:"prop_renames,omitempty"`
}

// UpgradeCheck computes a structural diff for each non-disabled node in doc
// against newSchema. oldSchema is used for stability/port comparisons.
// oldVersion and newVersion are version labels for the response.
func UpgradeCheck(doc GraphDocument, oldSchema, newSchema UpgradeSchemaPayload, oldVersion, newVersion string) UpgradeCheckResponse {
	var items []UpgradeItem

	for _, node := range doc.Nodes {
		if node.Disabled {
			continue
		}

		newDef, exists := newSchema.Components[node.Component]
		if !exists {
			items = append(items, UpgradeItem{
				NodeID:    node.ID,
				NodeLabel: node.Label,
				Component: node.Component,
				Class:     DiffComponentRemoved,
			})
			// No further checks for a removed component.
			continue
		}

		oldDef, hadOld := oldSchema.Components[node.Component]

		// Build new attribute/block name set for removal checks.
		// Block-typed props (arrays/objects keyed by block name) are excluded
		// from attr_removed — they are not flat attributes.
		newAttrSet := make(map[string]bool, len(newDef.Attributes)+len(newDef.Blocks))
		for _, a := range newDef.Attributes {
			newAttrSet[a.Name] = true
		}
		for _, b := range newDef.Blocks {
			newAttrSet[b.Name] = true
		}

		// attr_removed: node has a prop that no longer exists in new schema.
		for propName := range node.Props {
			if !newAttrSet[propName] {
				items = append(items, UpgradeItem{
					NodeID:    node.ID,
					NodeLabel: node.Label,
					Component: node.Component,
					Class:     DiffAttrRemoved,
					Detail:    propName,
				})
			}
		}

		// attr_added_required / enum_value_removed: scan new attributes.
		for _, attr := range newDef.Attributes {
			propVal, hasProp := node.Props[attr.Name]

			// attr_added_required: required attr missing or nil in node props.
			if attr.Required && (!hasProp || propVal == nil) {
				items = append(items, UpgradeItem{
					NodeID:    node.ID,
					NodeLabel: node.Label,
					Component: node.Component,
					Class:     DiffAttrAddedRequired,
					Detail:    attr.Name,
				})
				continue
			}

			// enum_value_removed: current value not in new enum set.
			if hasProp && len(attr.Values) > 0 {
				strVal := fmt.Sprint(propVal)
				if strVal != "" && !containsString(attr.Values, strVal) {
					items = append(items, UpgradeItem{
						NodeID:    node.ID,
						NodeLabel: node.Label,
						Component: node.Component,
						Class:     DiffEnumValueRemoved,
						Detail:    attr.Name + "=" + strVal,
					})
				}
			}
		}

		// port_type_changed: compare input/output port types by position.
		if hadOld {
			for i, newPort := range newDef.Inputs {
				if i < len(oldDef.Inputs) && oldDef.Inputs[i].Type != newPort.Type {
					items = append(items, UpgradeItem{
						NodeID:    node.ID,
						NodeLabel: node.Label,
						Component: node.Component,
						Class:     DiffPortTypeChanged,
						Detail:    fmt.Sprintf("%s → %s", oldDef.Inputs[i].Type, newPort.Type),
					})
				}
			}
			for i, newPort := range newDef.Outputs {
				if i < len(oldDef.Outputs) && oldDef.Outputs[i].Type != newPort.Type {
					items = append(items, UpgradeItem{
						NodeID:    node.ID,
						NodeLabel: node.Label,
						Component: node.Component,
						Class:     DiffPortTypeChanged,
						Detail:    fmt.Sprintf("%s → %s", oldDef.Outputs[i].Type, newPort.Type),
					})
				}
			}

			// stability_changed.
			if oldDef.Stability != newDef.Stability {
				items = append(items, UpgradeItem{
					NodeID:    node.ID,
					NodeLabel: node.Label,
					Component: node.Component,
					Class:     DiffStabilityChanged,
					Detail:    oldDef.Stability + " → " + newDef.Stability,
				})
			}
		}

		// migration_available: overlay provides a migration for this component.
		if mig, ok := newSchema.Migrations[node.Component]; ok {
			items = append(items, UpgradeItem{
				NodeID:    node.ID,
				NodeLabel: node.Label,
				Component: node.Component,
				Class:     DiffMigrationAvailable,
				Detail:    mig.To,
			})
		}
	}

	if items == nil {
		items = []UpgradeItem{}
	}

	return UpgradeCheckResponse{
		OldVersion:   oldVersion,
		NewVersion:   newVersion,
		Items:        items,
		NeedsUpgrade: len(items) > 0 || oldVersion != newVersion,
	}
}

// containsString reports whether ss contains s.
func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
