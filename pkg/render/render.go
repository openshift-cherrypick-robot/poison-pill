package render

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig"
	"github.com/pkg/errors"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
)

type RenderData struct {
	Funcs template.FuncMap
	Data  map[string]interface{}
}

type RenderConfig struct {
	*mcfgv1.ControllerConfigSpec
	PullSecret string
}

type DeviceInfo struct {
	PciAddress string
	NumVfs     int
}

const (
	filesDir          = "files"
	ovsUnitsDir       = "ovs-units"
	switchdevUnitsDir = "switchdev-units"
)

func MakeRenderData() RenderData {
	return RenderData{
		Funcs: template.FuncMap{},
		Data:  map[string]interface{}{},
	}
}

// RenderDir will render all manifests in a directory, descending in to subdirectories
// It will perform template substitutions based on the data supplied by the RenderData
func RenderDir(manifestDir string, d *RenderData) ([]*unstructured.Unstructured, error) {
	out := []*unstructured.Unstructured{}

	if err := filepath.Walk(manifestDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		// Skip non-manifest files
		if !(strings.HasSuffix(path, ".yml") || strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".json")) {
			return nil
		}

		objs, err := RenderTemplate(path, d)
		if err != nil {
			return err
		}
		out = append(out, objs...)
		return nil
	}); err != nil {
		return nil, errors.Wrap(err, "error rendering manifests")
	}

	return out, nil
}

// RenderTemplate reads, renders, and attempts to parse a yaml or
// json file representing one or more k8s api objects
func RenderTemplate(path string, d *RenderData) ([]*unstructured.Unstructured, error) {
	rendered, err := renderTemplate(path, d)
	if err != nil {
		return nil, err
	}

	out := []*unstructured.Unstructured{}

	// special case - if the entire file is whitespace, skip
	if len(strings.TrimSpace(rendered.String())) == 0 {
		return out, nil
	}

	decoder := yaml.NewYAMLOrJSONDecoder(rendered, 4096)
	for {
		u := unstructured.Unstructured{}
		if err := decoder.Decode(&u); err != nil {
			if err == io.EOF {
				break
			}
			return nil, errors.Wrapf(err, "failed to unmarshal manifest %s", path)
		}
		out = append(out, &u)
	}

	return out, nil
}

func renderTemplate(path string, d *RenderData) (*bytes.Buffer, error) {
	tmpl := template.New(path).Option("missingkey=error")
	if d.Funcs != nil {
		tmpl.Funcs(d.Funcs)
	}

	// Add universal functions
	tmpl.Funcs(template.FuncMap{"getOr": getOr, "isSet": isSet})
	tmpl.Funcs(sprig.TxtFuncMap())

	source, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read manifest %s", path)
	}

	if _, err := tmpl.Parse(string(source)); err != nil {
		return nil, errors.Wrapf(err, "failed to parse manifest %s as template", path)
	}

	rendered := bytes.Buffer{}
	if err := tmpl.Execute(&rendered, d.Data); err != nil {
		return nil, errors.Wrapf(err, "failed to render manifest %s", path)
	}

	return &rendered, nil
}

func formateDeviceList(devs []DeviceInfo) string {
	out := ""
	for _, dev := range devs {
		out = out + fmt.Sprintln(dev.PciAddress, dev.NumVfs)
	}
	return out
}

// existsDir returns true if path exists and is a directory, false if the path
// does not exist, and error if there is a runtime error or the path is not a directory
func existsDir(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, errors.Wrapf(err, "failed to open dir %q", path)
	}
	if !info.IsDir() {
		return false, errors.Wrapf(err, "expected template directory, %q is not a directory", path)
	}
	return true, nil
}

func filterTemplates(toFilter map[string]string, path string, d *RenderData) error {
	walkFn := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		// empty templates signify don't create
		if info.Size() == 0 {
			delete(toFilter, info.Name())
			return nil
		}

		// Render the template file
		renderedData, err := renderTemplate(path, d)
		if err != nil {
			return err
		}
		toFilter[info.Name()] = renderedData.String()
		return nil
	}

	return filepath.Walk(path, walkFn)
}
