package visual_test

import (
	"encoding/json"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/visual"
)

// itemsByClass is a test helper that filters upgrade items by class.
func itemsByClass(items []visual.UpgradeItem, class visual.DiffClass) []visual.UpgradeItem {
	var out []visual.UpgradeItem
	for _, it := range items {
		if it.Class == class {
			out = append(out, it)
		}
	}
	return out
}

var _ = Describe("UpgradeCheck", func() {
	// 7.4.5 — upgrade-check classifies every diff class

	var (
		oldSchema visual.UpgradeSchemaPayload
		newSchema visual.UpgradeSchemaPayload
	)

	BeforeEach(func() {
		oldData, err := os.ReadFile("testdata/schemas/v_old.json")
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(oldData, &oldSchema)).To(Succeed())

		newData, err := os.ReadFile("testdata/schemas/v_new.json")
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(newData, &newSchema)).To(Succeed())
	})

	makeDoc := func(component, label string, props map[string]any) visual.GraphDocument {
		return visual.GraphDocument{
			Kind:          "alloy-graph/v1",
			SchemaVersion: "test-v1.0.0",
			Nodes: []visual.GraphNode{{
				ID:        "n1",
				Component: component,
				Label:     label,
				Props:     props,
			}},
		}
	}

	It("7.4.5.1 — component_removed: finds item for removed component", func() {
		doc := makeDoc("test.removed_component", "removed", nil)
		result := visual.UpgradeCheck(doc, oldSchema, newSchema, "test-v1.0.0", "test-v2.0.0")
		Expect(result.NeedsUpgrade).To(BeTrue())
		Expect(result.Items).To(ContainElement(
			HaveField("Class", visual.DiffComponentRemoved),
		))
		// Red run: comment out DiffComponentRemoved branch → this expectation fails.
	})

	It("7.4.5.2 — attr_removed: finds item for old_attr prop no longer in schema", func() {
		doc := makeDoc("test.keep_component", "keep", map[string]any{
			"url":      "http://x",
			"old_attr": "something",
		})
		result := visual.UpgradeCheck(doc, oldSchema, newSchema, "test-v1.0.0", "test-v2.0.0")
		items := itemsByClass(result.Items, visual.DiffAttrRemoved)
		Expect(items).To(HaveLen(1))
		Expect(items[0].Detail).To(Equal("old_attr"))
	})

	It("7.4.5.3 — attr_added_required: finds item for new_required_attr not in node props", func() {
		doc := makeDoc("test.keep_component", "keep", map[string]any{"url": "http://x"})
		result := visual.UpgradeCheck(doc, oldSchema, newSchema, "test-v1.0.0", "test-v2.0.0")
		items := itemsByClass(result.Items, visual.DiffAttrAddedRequired)
		Expect(items).To(ContainElement(
			HaveField("Detail", "new_required_attr"),
		))
		// Red run: remove required-attr detection → expectation fails.
	})

	It("7.4.5.4 — enum_value_removed: finds item when prop has value removed from enum", func() {
		doc := makeDoc("test.keep_component", "keep", map[string]any{
			"url":  "http://x",
			"mode": "pull", // "pull" removed in v_new; only "push" remains
		})
		result := visual.UpgradeCheck(doc, oldSchema, newSchema, "test-v1.0.0", "test-v2.0.0")
		items := itemsByClass(result.Items, visual.DiffEnumValueRemoved)
		Expect(items).To(ContainElement(
			HaveField("Detail", ContainSubstring("pull")),
		))
	})

	It("7.4.5.5 — port_type_changed: finds item for test.port_component input change", func() {
		doc := makeDoc("test.port_component", "port", nil)
		result := visual.UpgradeCheck(doc, oldSchema, newSchema, "test-v1.0.0", "test-v2.0.0")
		items := itemsByClass(result.Items, visual.DiffPortTypeChanged)
		Expect(items).To(HaveLen(1))
		Expect(items[0].Detail).To(ContainSubstring("targets"))
		Expect(items[0].Detail).To(ContainSubstring("loki.logs"))
	})

	It("7.4.5.6 — stability_changed: finds item for test.stable_component public-preview→ga", func() {
		doc := makeDoc("test.stable_component", "stable", nil)
		result := visual.UpgradeCheck(doc, oldSchema, newSchema, "test-v1.0.0", "test-v2.0.0")
		items := itemsByClass(result.Items, visual.DiffStabilityChanged)
		Expect(items).To(HaveLen(1))
		Expect(items[0].Detail).To(ContainSubstring("public-preview"))
		Expect(items[0].Detail).To(ContainSubstring("ga"))
	})

	It("7.4.5.7 — migration_available: finds item for test.migrate_component", func() {
		doc := makeDoc("test.migrate_component", "migrate", map[string]any{"old_url": "http://x"})
		result := visual.UpgradeCheck(doc, oldSchema, newSchema, "test-v1.0.0", "test-v2.0.0")
		items := itemsByClass(result.Items, visual.DiffMigrationAvailable)
		Expect(items).To(HaveLen(1))
		Expect(items[0].Detail).To(ContainSubstring("test.migrate_component"))
	})

	It("disabled nodes are skipped", func() {
		doc := visual.GraphDocument{
			Kind:          "alloy-graph/v1",
			SchemaVersion: "test-v1.0.0",
			Nodes: []visual.GraphNode{{
				ID:        "n1",
				Component: "test.removed_component",
				Label:     "removed",
				Disabled:  true,
			}},
		}
		result := visual.UpgradeCheck(doc, oldSchema, newSchema, "test-v1.0.0", "test-v2.0.0")
		Expect(result.Items).To(BeEmpty())
		// NeedsUpgrade is still true because versions differ.
		Expect(result.NeedsUpgrade).To(BeTrue())
	})
})
