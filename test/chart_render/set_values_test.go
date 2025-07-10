package chart_render_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"

	"github.com/werf/nelm/pkg/action"
)

var _ = Describe("Chart Render Set Values", func() {
	var (
		ctx          context.Context
		tempDir      string
		chartDir     string
		templateFile string
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tempDir, err = os.MkdirTemp("", "nelm-test-")
		Expect(err).NotTo(HaveOccurred())

		chartDir = filepath.Join(tempDir, "test-chart")
		err = os.MkdirAll(chartDir, 0755)
		Expect(err).NotTo(HaveOccurred())

		// Create Chart.yaml
		chartYaml := `apiVersion: v2
name: test-chart
description: A test chart for set values
type: application
version: 0.1.0
appVersion: "1.0"
`
		err = os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte(chartYaml), 0644)
		Expect(err).NotTo(HaveOccurred())

		// Create templates directory
		templatesDir := filepath.Join(chartDir, "templates")
		err = os.MkdirAll(templatesDir, 0755)
		Expect(err).NotTo(HaveOccurred())

		// Create test template that outputs values as YAML to test parsing
		templateFile = filepath.Join(templatesDir, "test.yaml")
		template := `{{ fail ($.Values | toYaml) }}`
		err = os.WriteFile(templateFile, []byte(template), 0644)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if tempDir != "" {
			os.RemoveAll(tempDir)
		}
	})

	Context("when parsing --set values with commas", func() {
		It("should handle comma-separated values without requiring extra escaping", func() {
			opts := action.ChartRenderOptions{
				Chart:            chartDir,
				ReleaseName:      "test-release",
				ReleaseNamespace: "test-namespace",
				ValuesSets:       []string{"name=value1\\,value2"},
				Remote:           false,
			}

			_, err := action.ChartRender(ctx, opts)
			Expect(err).To(HaveOccurred()) // We expect it to fail with our template

			// The error should contain the parsed values
			errorMsg := err.Error()
			Expect(errorMsg).To(ContainSubstring("name: value1,value2"))
		})

		It("should parse the same as helm with escaped commas", func() {
			// This is how helm expects it: helm template --set "name=value1\,value2" .
			opts := action.ChartRenderOptions{
				Chart:            chartDir,
				ReleaseName:      "test-release",
				ReleaseNamespace: "test-namespace",
				ValuesSets:       []string{"name=value1\\,value2"},
				Remote:           false,
			}

			_, err := action.ChartRender(ctx, opts)
			Expect(err).To(HaveOccurred())

			errorMsg := err.Error()
			// Should parse as a single value with comma, not as separate values
			Expect(errorMsg).To(ContainSubstring("name: value1,value2"))
			Expect(errorMsg).NotTo(ContainSubstring("value2:")) // Should not create separate key
		})

		It("should reject the old workaround with double escaping", func() {
			// This was the old workaround that shouldn't be needed anymore
			opts := action.ChartRenderOptions{
				Chart:            chartDir,
				ReleaseName:      "test-release",
				ReleaseNamespace: "test-namespace",
				ValuesSets:       []string{"\"name=value1\\,value2\""},
				Remote:           false,
			}

			_, err := action.ChartRender(ctx, opts)
			Expect(err).To(HaveOccurred())

			// This should NOT work anymore - the quotes become part of the key name
			errorMsg := err.Error()
			Expect(errorMsg).To(ContainSubstring("'\"name': value1,value2\""))
		})

		It("should handle multiple comma-separated values", func() {
			opts := action.ChartRenderOptions{
				Chart:            chartDir,
				ReleaseName:      "test-release",
				ReleaseNamespace: "test-namespace",
				ValuesSets:       []string{"list={value1\\,value2,value3\\,value4}"},
				Remote:           false,
			}

			_, err := action.ChartRender(ctx, opts)
			Expect(err).To(HaveOccurred())

			errorMsg := err.Error()
			// Should parse as array with comma-containing values
			Expect(errorMsg).To(ContainSubstring("list:"))
			Expect(errorMsg).To(ContainSubstring("value1,value2"))
			Expect(errorMsg).To(ContainSubstring("value3,value4"))
		})

		It("should demonstrate that nelm now parses the same as helm", func() {
			testCases := []struct {
				name          string
				setValue      string
				expectContain string
				description   string
			}{
				{
					name:          "helm_style_single_escape",
					setValue:      "name=value1\\,value2",
					expectContain: "name: value1,value2",
					description:   "How helm expects escaped commas - now works in nelm",
				},
				{
					name:          "multiple_keys_with_commas",
					setValue:      "key1=val1\\,val2,key2=val3\\,val4",
					expectContain: "key1: val1,val2",
					description:   "Multiple keys each with comma-separated values",
				},
				{
					name:          "single_key_multiple_comma_values",
					setValue:      "list={item1\\,with\\,commas,item2\\,also\\,commas}",
					expectContain: "list:",
					description:   "Single key with multiple comma-containing values",
				},
			}

			for _, tc := range testCases {
				By(tc.description)
				opts := action.ChartRenderOptions{
					Chart:            chartDir,
					ReleaseName:      "test-release",
					ReleaseNamespace: "test-namespace",
					ValuesSets:       []string{tc.setValue},
					Remote:           false,
				}

				_, err := action.ChartRender(ctx, opts)
				Expect(err).To(HaveOccurred()) // We expect failure due to our test template

				errorMsg := err.Error()
				Expect(errorMsg).To(ContainSubstring(tc.expectContain))
			}
		})
	})

	Context("when comparing with direct helm behavior", func() {
		It("should document expected helm parsing behavior", func() {
			// This test documents what helm does with these values
			// We'll create a simple values.yaml test to show expected behavior

			valuesFile := filepath.Join(chartDir, "values.yaml")
			defaultValues := `name: default-value
list: []
`
			err := os.WriteFile(valuesFile, []byte(defaultValues), 0644)
			Expect(err).NotTo(HaveOccurred())

			// Test with a simple template that doesn't fail
			simpleTemplate := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-config
data:
  name: {{ .Values.name | quote }}
  {{- if .Values.list }}
  list: |
    {{- range .Values.list }}
    - {{ . | quote }}
    {{- end }}
  {{- end }}
`
			err = os.WriteFile(templateFile, []byte(simpleTemplate), 0644)
			Expect(err).NotTo(HaveOccurred())

			opts := action.ChartRenderOptions{
				Chart:            chartDir,
				ReleaseName:      "test-release",
				ReleaseNamespace: "test-namespace",
				ValuesSets:       []string{"name=value1\\,value2"},
				Remote:           false,
				OutputNoPrint:    true,
			}

			result, err := action.ChartRender(ctx, opts)
			Expect(err).NotTo(HaveOccurred())

			// Convert result to YAML to inspect
			Expect(result.Resources).To(HaveLen(1))
			resourceYaml, err := yaml.Marshal(result.Resources[0])
			Expect(err).NotTo(HaveOccurred())

			GinkgoWriter.Printf("Rendered resource:\n%s\n", string(resourceYaml))

			// The name field should contain "value1,value2" as a single value
			resourceStr := string(resourceYaml)
			Expect(resourceStr).To(ContainSubstring("value1,value2"))
		})
	})
})

func TestChartRenderSetValues(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Chart Render Set Values Suite")
}
