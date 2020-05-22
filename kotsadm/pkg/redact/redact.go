package redact

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/replicatedhq/kots/pkg/util"
	"github.com/replicatedhq/troubleshoot/pkg/apis/troubleshoot/v1beta1"
	"github.com/replicatedhq/troubleshoot/pkg/client/troubleshootclientset/scheme"
	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
	kuberneteserrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

func init() {
	scheme.AddToScheme(scheme.Scheme)
}

type RedactorList struct {
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Created     time.Time `json:"createdAt"`
	Updated     time.Time `json:"updatedAt"`
	Enabled     bool      `json:"enabled"`
	Description string    `json:"description"`
}

type RedactorMetadata struct {
	Metadata RedactorList `json:"metadata"`

	Redact v1beta1.Redact `json:"redact"`
}

// GetRedactSpec returns the redaction yaml spec, a pretty error string, and the underlying error
func GetRedactSpec() (string, string, error) {
	configMap, errstr, err := getConfigmap()
	if err != nil || configMap == nil {
		return "", errstr, err
	}

	redactObj, err := buildFullRedact(configMap)
	if err != nil {
		return "", "failed to build full redact yaml", err
	}

	yamlBytes, err := util.MarshalIndent(2, redactObj)
	if err != nil {
		return "", "failed to render full redact yaml", err
	}
	return string(yamlBytes), "", nil
}

func GetRedact() (*v1beta1.Redactor, error) {
	configmap, _, err := getConfigmap()
	if err != nil {
		return nil, err
	}
	if configmap == nil {
		return nil, nil
	}

	return buildFullRedact(configmap)
}

func GetRedactInfo() ([]RedactorList, error) {
	configmap, _, err := getConfigmap()
	if err != nil {
		return nil, errors.Wrap(err, "get redactors configmap")
	}
	if configmap == nil {
		return nil, nil
	}

	if combinedYaml, ok := configmap.Data["kotsadm-redact"]; ok {
		// this is the key used for the combined redact list, so run the migration
		newMap, err := splitRedactors(combinedYaml, configmap.Data)
		if err != nil {
			return nil, errors.Wrap(err, "failed to split combined redactors")
		}
		configmap.Data = newMap

		// now that the redactors have been split, save the configmap
		configmap, err = writeConfigmap(configmap)
		if err != nil {
			return nil, errors.Wrap(err, "failed to update configmap")
		}
	}

	list := []RedactorList{}

	for k, v := range configmap.Data {
		redactorEntry := RedactorMetadata{}
		err = json.Unmarshal([]byte(v), &redactorEntry)
		if err != nil {
			return nil, errors.Wrapf(err, "unable to parse key %s", k)
		}
		list = append(list, redactorEntry.Metadata)
	}
	return list, nil
}

func GetRedactBySlug(slug string) (*RedactorMetadata, error) {
	configmap, _, err := getConfigmap()
	if err != nil {
		return nil, err
	}
	if configmap == nil {
		return nil, fmt.Errorf("configmap not found")
	}

	redactString, ok := configmap.Data[slug]
	if !ok {
		return nil, fmt.Errorf("redactor %s not found", slug)
	}

	redactorEntry := RedactorMetadata{}
	err = json.Unmarshal([]byte(redactString), &redactorEntry)
	if err != nil {
		return nil, errors.Wrapf(err, "unable to parse redactor %s", slug)
	}

	return &redactorEntry, nil
}

// SetRedactSpec sets the global redact spec to the specified string, and returns a pretty error string + the underlying error
func SetRedactSpec(spec string) (string, error) {
	cfg, err := config.GetConfig()
	if err != nil {
		return "failed to get cluster config", errors.Wrap(err, "failed to get cluster config")
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "failed to create kubernetes clientset", errors.Wrap(err, "failed to create kubernetes clientset")
	}

	configMap, errMsg, err := getConfigmap()
	if err != nil {
		return errMsg, err
	}

	newMap, err := splitRedactors(spec, configMap.Data)
	if err != nil {
		return "failed to split redactors", errors.Wrap(err, "failed to split redactors")
	}

	configMap.Data = newMap
	_, err = clientset.CoreV1().ConfigMaps(os.Getenv("POD_NAMESPACE")).Update(configMap)
	if err != nil {
		return "failed to update kotsadm-redact configMap", errors.Wrap(err, "failed to update kotsadm-redact configMap")
	}
	return "", nil
}

func SetRedactMetadata(name, slug, description string, enabled bool) (*RedactorList, error) {
	configMap, _, err := getConfigmap()
	if err != nil {
		return nil, err
	}

	redactString, ok := configMap.Data[slug]
	if !ok {
		return nil, fmt.Errorf("slug %s not found", slug)
	}

	redactorEntry := RedactorMetadata{}
	err = json.Unmarshal([]byte(redactString), &redactorEntry)
	if err != nil {
		return nil, errors.Wrapf(err, "unable to parse redactor %s", slug)
	}

	redactorEntry.Metadata.Updated = time.Now()
	redactorEntry.Metadata.Description = description
	redactorEntry.Metadata.Enabled = enabled
	redactorEntry.Metadata.Name = name

	if slug != getSlug(name) && name != "" {
		// changing name
		delete(configMap.Data, slug)
		slug = getSlug(name)
		redactorEntry.Metadata.Slug = slug
		redactorEntry.Metadata.Name = name
	}

	jsonBytes, err := json.Marshal(redactorEntry)
	if err != nil {
		return nil, errors.Wrapf(err, "unable to marshal redactor %s", name)
	}

	configMap.Data[slug] = string(jsonBytes)

	_, err = writeConfigmap(configMap)
	if err != nil {
		return nil, errors.Wrapf(err, "write configMap with updated metadata")
	}
	return &redactorEntry.Metadata, nil
}

func SetRedactYaml(slug string, yamlBytes []byte) (*RedactorList, error) {
	// parse yaml as redactor
	newRedactorSpec := v1beta1.Redact{}
	err := yaml.Unmarshal(yamlBytes, &newRedactorSpec)
	if err != nil {
		return nil, errors.Wrapf(err, "unable to parse new redact yaml")
	}

	configMap, _, err := getConfigmap()
	if err != nil {
		return nil, err
	}

	redactorEntry := RedactorMetadata{}
	redactString, ok := configMap.Data[slug]
	if !ok {
		// new redactor
		redactorEntry.Metadata = RedactorList{
			Name:        getSlug(newRedactorSpec.Name),
			Slug:        slug,
			Created:     time.Now(),
			Updated:     time.Now(),
			Enabled:     true,
			Description: "",
		}
		if redactorEntry.Metadata.Name == "" {
			newRedactorSpec.Name = slug
			redactorEntry.Metadata.Name = slug
		}
	} else {
		// unmarshal existing redactor, check if name changed
		err = json.Unmarshal([]byte(redactString), &redactorEntry)
		if err != nil {
			return nil, errors.Wrapf(err, "unable to parse redactor %s", slug)
		}
		redactorEntry.Metadata.Updated = time.Now()

		if slug != getSlug(newRedactorSpec.Name) && newRedactorSpec.Name != "" {
			// changing name
			delete(configMap.Data, slug)
			slug = getSlug(newRedactorSpec.Name)
			redactorEntry.Metadata.Slug = slug
			redactorEntry.Metadata.Name = newRedactorSpec.Name
		}

		if newRedactorSpec.Name == "" {
			newRedactorSpec.Name = slug
			redactorEntry.Metadata.Name = slug
		}
	}

	redactorEntry.Redact = newRedactorSpec

	jsonBytes, err := json.Marshal(redactorEntry)
	if err != nil {
		return nil, errors.Wrapf(err, "unable to marshal redactor %s", slug)
	}

	configMap.Data[slug] = string(jsonBytes)

	_, err = writeConfigmap(configMap)
	if err != nil {
		return nil, errors.Wrapf(err, "write configMap with updated redact")
	}
	return &redactorEntry.Metadata, nil
}

func DeleteRedact(slug string) error {
	configMap, _, err := getConfigmap()
	if err != nil {
		return err
	}

	delete(configMap.Data, slug)

	_, err = writeConfigmap(configMap)
	if err != nil {
		return errors.Wrapf(err, "write configMap with updated redact")
	}
	return nil
}

func getConfigmap() (*v1.ConfigMap, string, error) {
	cfg, err := config.GetConfig()
	if err != nil {
		return nil, "failed to get cluster config", errors.Wrap(err, "failed to get cluster config")
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, "failed to create kubernetes clientset", errors.Wrap(err, "failed to create kubernetes clientset")
	}

	configMap, err := clientset.CoreV1().ConfigMaps(os.Getenv("POD_NAMESPACE")).Get("kotsadm-redact", metav1.GetOptions{})
	if err != nil {
		if !kuberneteserrors.IsNotFound(err) {
			// not a not found error, so a real error
			return nil, "failed to get kotsadm-redact configMap", errors.Wrap(err, "failed to get kotsadm-redact configMap")
		} else {
			// not found, so create one and return it
			newMap := v1.ConfigMap{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ConfigMap",
					APIVersion: "v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kotsadm-redact",
					Namespace: os.Getenv("POD_NAMESPACE"),
					Labels: map[string]string{
						"kots.io/kotsadm": "true",
					},
				},
				Data: map[string]string{},
			}
			createdMap, err := clientset.CoreV1().ConfigMaps(os.Getenv("POD_NAMESPACE")).Create(&newMap)
			if err != nil {
				return nil, "failed to create kotsadm-redact configMap", errors.Wrap(err, "failed to create kotsadm-redact configMap")
			}

			return createdMap, "", nil
		}
	}
	return configMap, "", nil
}

func writeConfigmap(configMap *v1.ConfigMap) (*v1.ConfigMap, error) {
	cfg, err := config.GetConfig()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get cluster config")
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create kubernetes clientset")
	}

	newConfigMap, err := clientset.CoreV1().ConfigMaps(os.Getenv("POD_NAMESPACE")).Update(configMap)
	if err != nil {
		return nil, errors.Wrap(err, "failed to update configmap")
	}
	return newConfigMap, nil
}

func getSlug(name string) string {
	name = strings.ReplaceAll(name, " ", "-")

	name = regexp.MustCompile(`[^\w\d-_]`).ReplaceAllString(name, "")
	return name
}

func buildFullRedact(config *v1.ConfigMap) (*v1beta1.Redactor, error) {
	full := &v1beta1.Redactor{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Redactor",
			APIVersion: "troubleshoot.replicated.com/v1beta1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "kotsadm-redact",
		},
		Spec: v1beta1.RedactorSpec{},
	}

	for k, v := range config.Data {
		if k == "kotsadm-redact" {
			// this is the key used for the combined redact list
			decode := scheme.Codecs.UniversalDeserializer().Decode
			obj, _, err := decode([]byte(v), nil, nil)
			if err != nil {
				return nil, errors.Wrap(err, "deserialize combined redact spec")
			}
			redactor, ok := obj.(*v1beta1.Redactor)
			if ok && redactor != nil {
				full.Spec.Redactors = append(full.Spec.Redactors, redactor.Spec.Redactors...)
			}
			continue
		}

		redactorEntry := RedactorMetadata{}
		err := json.Unmarshal([]byte(v), &redactorEntry)
		if err != nil {
			return nil, errors.Wrapf(err, "unable to parse key %s", k)
		}
		if redactorEntry.Metadata.Enabled {
			full.Spec.Redactors = append(full.Spec.Redactors, &redactorEntry.Redact)
		}
	}
	return full, nil
}

func splitRedactors(spec string, existingMap map[string]string) (map[string]string, error) {
	fmt.Printf("running migration from combined kotsadm-redact doc")

	decode := scheme.Codecs.UniversalDeserializer().Decode
	obj, _, err := decode([]byte(spec), nil, nil)
	if err != nil {
		return nil, errors.Wrap(err, "deserialize combined redact spec")
	}
	redactor, ok := obj.(*v1beta1.Redactor)
	if !ok {
		return nil, errors.Wrap(err, "combined redact spec at kotsadm-redact is not a redactor")
	}

	for idx, redactorSpec := range redactor.Spec.Redactors {
		if redactorSpec == nil {
			continue
		}

		redactorName := ""
		if redactorSpec.Name != "" {
			redactorName = redactorSpec.Name
		} else {
			redactorName = fmt.Sprintf("redactor-%d", idx)
			redactorSpec.Name = redactorName
		}

		newRedactor := RedactorMetadata{
			Metadata: RedactorList{
				Name:    redactorName,
				Slug:    getSlug(redactorName),
				Created: time.Now(),
				Updated: time.Now(),
				Enabled: true,
			},
			Redact: *redactorSpec,
		}

		jsonBytes, err := json.Marshal(newRedactor)
		if err != nil {
			return nil, errors.Wrapf(err, "unable to marshal redactor %s", redactorName)
		}

		existingMap[newRedactor.Metadata.Slug] = string(jsonBytes)
	}
	delete(existingMap, "kotsadm-redact")

	return existingMap, nil
}
