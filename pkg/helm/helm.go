package helm

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"

	"github.com/topfreegames/helm-generate/pkg/util"

	"gopkg.in/yaml.v2"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/postrender"
)

// Configurator defines the interface for implementing a Helm Configuration
type Configurator interface {
	getConf(filename string) error
	BuildHelmConfig(path string) error
	InstallChart(vals chartutil.Values) ([]map[string]interface{}, error)

	buildHelmClient(name string, namespace string) (*action.Install, error)
	loadChart(client *action.Install) (*chart.Chart, error)
}

// Configuration defines a struct for the .helm.yaml file
type Configuration struct {
	Chart            string `yaml:"chart"`
	ChartVersion     string `yaml:"chartVersion"`
	HelmYaml         string
	ValuesYaml       string
	PostRenderBinary string `yaml:"postRenderBinary"`
}

func addNamespaceMetadata(manifests []map[string]interface{}, namespace string) ([]map[string]interface{}, error) {
	if len(manifests) == 0 {
		return nil, fmt.Errorf("Empty manifest list")
	}
	for _, manifest := range manifests {
		_, err := util.NestedMapLookup(manifest, "metadata")
		if err == nil {
			manifest["metadata"].(map[interface{}]interface{})["namespace"] = namespace
		} else {
			return nil, fmt.Errorf("Required field not found: %w", err)
		}
	}
	return manifests, nil
}

// getConf reads .helm.yaml values from a file and loads them into a structure
func (h *Configuration) getConf(file io.Reader) error {
	if file == nil {
		return os.ErrNotExist
	}
	buffer, err := ioutil.ReadAll(file)
	if err != nil {
		return fmt.Errorf("Error reading IO buffer: %w", err)
	}
	err = yaml.Unmarshal(buffer, h)
	if err != nil {
		return fmt.Errorf("An error occured unmarshaling the file contents into a YAML struct: %s", err)
	}
	return nil
}

// BuildHelmConfig overrides the values of the Conf based on a .helm.yaml file,
// it also checks if all required attributes are set.
func (h *Configuration) BuildHelmConfig(file io.Reader) error {
	err := h.getConf(file)
	if os.IsNotExist(err) {
		if h.Chart == "" {
			return fmt.Errorf("Required configuration for default helm chart missing")
		}
		if h.ChartVersion == "" {
			return fmt.Errorf("Required configuration for default helm chart version missing")
		}
	} else if err != nil {
		return err
	}
	return nil
}

func (h *Configuration) buildHelmClient(name string, namespace string) (*action.Install, error) {
	settings := cli.New()
	actionConfig := new(action.Configuration)
	//nolint:errcheck
	actionConfig.Init(settings.RESTClientGetter(), namespace, os.Getenv("HELM_DRIVER"), log.Printf)
	client := action.NewInstall(actionConfig)
	client.ReleaseName = name
	client.Namespace = namespace
	client.DryRun = true
	client.ClientOnly = true
	client.UseReleaseName = true
	if h.PostRenderBinary != "" {
		pe, err := postrender.NewExec(h.PostRenderBinary)
		if err != nil {
			return nil, fmt.Errorf("Invalid configuration of postrenderer binary: %s", err)
		}
		client.PostRenderer = pe
	}

	return client, nil
}

func (h *Configuration) loadChart(client *action.Install) (*chart.Chart, error) {
	settings := cli.New()
	client.ChartPathOptions.Version = h.ChartVersion
	cp, err := client.ChartPathOptions.LocateChart(h.Chart, settings)
	if err != nil {
		return nil, fmt.Errorf("Unable to locate chart: %s", err)
	}
	return loader.Load(cp)
}

// InstallChart uses the Helm sdk and Conf values to generate the Chart manifests
func (h *Configuration) InstallChart(vals chartutil.Values) ([]map[string]interface{}, error) {
	// Validate chart
	values, err := util.ValidateValues(vals, "releaseName", "namespace")
	if err != nil {
		return nil, err
	}

	name := values["releaseName"].(string)
	namespace := values["namespace"].(string)

	// template helm chart
	client, err := h.buildHelmClient(name, namespace)
	if err != nil {
		return nil, err
	}
	chartRequested, err := h.loadChart(client)
	if err != nil {
		return nil, err
	}
	output, err := client.Run(chartRequested, vals)
	if err != nil {
		return nil, fmt.Errorf("Failed to generate chart: %s", err)
	}
	manifest, err := util.DecodeYamls(output.Manifest)
	if err != nil {
		return nil, err
	}

	// One current limitation on the way Helm Releases work is that the namespace is
	// provided as a parameter to kubectl.
	// Helm templates also don't generate the namespace
	// So we're going to add a Namespace manifest
	nsManifest := []map[string]interface{}{util.CreateNamespace(namespace, nil, nil)}
	manifest, err = addNamespaceMetadata(manifest, namespace)
	if err != nil {
		return nil, err
	}
	return append(nsManifest, manifest...), nil
}
