package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"

	"github.com/argoproj/argo-cd/v2/pkg/apis/application"
	argov1alpha1 "github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

const (
	separatorPanic  = ": "
	separatorYAML   = "---\n"
	yamlStatusField = "status"
)

type accessLevel int

const (
	ReadOnly accessLevel = iota
	ReadSync
)

func (a accessLevel) String() string {
	switch a {
	case ReadOnly:
		return "read-only"
	case ReadSync:
		return "read-sync"
	default:
		panic(fmt.Sprintf("unknown access level %d", a))
	}
}

func (a accessLevel) Policies(appProjectName string) []string {
	switch a {
	case ReadOnly:
		return []string{
			fmt.Sprintf("p, proj:%s:read-only, *, get, %s/*, allow", appProjectName, appProjectName),
		}
	case ReadSync:
		return []string{
			fmt.Sprintf("p, proj:%s:read-sync, applications, action/apps/Deployment/restart, %s/*, allow", appProjectName, appProjectName),
			fmt.Sprintf("p, proj:%s:read-sync, applications, action/argoproj.io/Rollout/abort, %s/*, allow", appProjectName, appProjectName),
			fmt.Sprintf("p, proj:%s:read-sync, applications, action/argoproj.io/Rollout/promote-full, %s/*, allow", appProjectName, appProjectName),
			fmt.Sprintf("p, proj:%s:read-sync, applications, action/argoproj.io/Rollout/restart, %s/*, allow", appProjectName, appProjectName),
			fmt.Sprintf("p, proj:%s:read-sync, applications, action/argoproj.io/Rollout/resume, %s/*, allow", appProjectName, appProjectName),
			fmt.Sprintf("p, proj:%s:read-sync, applications, action/argoproj.io/Rollout/retry, %s/*, allow", appProjectName, appProjectName),
			fmt.Sprintf("p, proj:%s:read-sync, applications, sync, %s/*, allow", appProjectName, appProjectName),
			fmt.Sprintf("g, proj:%s:read-sync, proj:%s:read-only", appProjectName, appProjectName),
		}
	default:
		panic(fmt.Sprintf("unknown access level %d", a))
	}
}

type ArgoCDProject struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ProjectSpec `json:"spec,omitempty"`
}

type ProjectSpec struct {
	AccessControl        AppProjectAccessControl    `json:"accessControl,omitempty"`
	Environment          string                     `json:"environment,omitempty"`
	AppProject           argov1alpha1.AppProject    `json:"appProjectTemplate,omitempty"`
	ApplicationTemplates []argov1alpha1.Application `json:"applicationTemplates,omitempty"`
}

type AppProjectAccessControl struct {
	ReadOnly []string `json:"ReadOnly,omitempty"`
	ReadSync []string `json:"ReadSync,omitempty"`
}

func main() {
	filePath := os.Args[1]

	data, err := ioutil.ReadFile(filePath)
	if err != nil {
		log.Panic(filePath, separatorPanic, err)
	}

	if err := GenerateManifests(data, os.Stdout); err != nil {
		log.Panic(filePath, separatorPanic, err)
	}
}

func GenerateManifests(data []byte, out io.Writer) error {
	var argocdProject ArgoCDProject
	if err := yaml.Unmarshal(data, &argocdProject); err != nil {
		return err
	}

	appProject := extractAppProject(&argocdProject)

	b, err := marshalYAMLWithoutStatusField(appProject)
	if err != nil {
		return err
	}
	if _, err := out.Write(b); err != nil {
		return err
	}

	apps := extractApplications(argocdProject, appProject)
	for _, app := range apps {
		if _, err := out.Write([]byte(separatorYAML)); err != nil {
			return err
		}

		b, err := marshalYAMLWithoutStatusField(app)
		if err != nil {
			return err
		}
		if _, err := out.Write(b); err != nil {
			return err
		}
	}

	return nil
}

func extractAppProject(argocdProject *ArgoCDProject) *argov1alpha1.AppProject {
	appProject := &argocdProject.Spec.AppProject

	appProject.TypeMeta = metav1.TypeMeta{
		APIVersion: argov1alpha1.SchemeGroupVersion.String(),
		Kind:       application.AppProjectKind,
	}

	if appProject.Name == "" {
		appProject.Name = argocdProject.Name
	}

	appProject.Spec.NamespaceResourceWhitelist = []metav1.GroupKind{
		metav1.GroupKind{
			Group: "*",
			Kind:  "*",
		},
	}

	appProject.Spec.SourceRepos = []string{
		"*",
	}

	if appProject.Spec.Destinations == nil {
		destinationMap := make(map[string]argov1alpha1.ApplicationDestination)
		for _, app := range argocdProject.Spec.ApplicationTemplates {
			destinationMap[app.Spec.Destination.String()] = app.Spec.Destination
		}

		destinations := make([]argov1alpha1.ApplicationDestination, 0, len(destinationMap))
		for _, destination := range destinationMap {
			destinations = append(destinations, destination)
		}
		appProject.Spec.Destinations = destinations
	}

	readOnlyProjectRole := makeProjectRole(ReadOnly, argocdProject, appProject)
	appProject.Spec.Roles = append(appProject.Spec.Roles, *readOnlyProjectRole)

	readSyncProjectRole := makeProjectRole(ReadSync, argocdProject, appProject)
	appProject.Spec.Roles = append(appProject.Spec.Roles, *readSyncProjectRole)

	return appProject
}

func makeProjectRole(accessLevel accessLevel, argocdProject *ArgoCDProject, appProject *argov1alpha1.AppProject) *argov1alpha1.ProjectRole {
	var groups []string
	switch accessLevel {
	case ReadOnly:
		groups = argocdProject.Spec.AccessControl.ReadOnly
	case ReadSync:
		groups = argocdProject.Spec.AccessControl.ReadSync
	}

	return &argov1alpha1.ProjectRole{
		Name:     accessLevel.String(),
		Policies: accessLevel.Policies(appProject.Name),
		Groups:   groups,
	}
}

func extractApplications(argocdProject ArgoCDProject, appProject *argov1alpha1.AppProject) []argov1alpha1.Application {
	apps := argocdProject.Spec.ApplicationTemplates

	for i := range apps {
		app := &apps[i]

		app.TypeMeta = metav1.TypeMeta{
			APIVersion: argov1alpha1.SchemeGroupVersion.String(),
			Kind:       application.ApplicationKind,
		}

		app.Spec.Project = appProject.Name

		if argocdProject.Spec.Environment != "" {
			app.Spec.Source.Path = fmt.Sprintf("./k8s/overlays/%s", argocdProject.Spec.Environment)
			app.Spec.Source.TargetRevision = fmt.Sprintf("env-%s", argocdProject.Spec.Environment)
		}
	}

	return apps
}

func marshalYAMLWithoutStatusField(v interface{}) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	var vm map[string]interface{}
	if err := json.Unmarshal(b, &vm); err != nil {
		return nil, err
	}

	delete(vm, yamlStatusField)

	return yaml.Marshal(vm)
}