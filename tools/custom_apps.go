package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/truenas/truenas-mcp/truenas"
)

// Custom apps are TrueNAS apps defined by a docker compose document instead
// of a catalog template. install_app only handles catalog apps, and update_app
// cannot update custom apps (it never sends custom_compose_config), so this
// tool covers both.
//
// The compose document is taken as an object (YAML's JSON form) so it can be
// validated here without a YAML dependency. Bind mounts must stay under /mnt/,
// matching the host-path policy enforced for catalog apps.

// validateCustomCompose checks a compose document and returns a sorted list of
// warnings for settings that weaken isolation. It returns an error for
// documents that are malformed or bind-mount host paths outside /mnt/.
func validateCustomCompose(compose map[string]interface{}) ([]string, error) {
	services, ok := compose["services"].(map[string]interface{})
	if !ok || len(services) == 0 {
		return nil, fmt.Errorf("compose must contain a non-empty services object")
	}

	var warnings []string
	for name, raw := range services {
		svc, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("services.%s must be an object", name)
		}
		if image, _ := svc["image"].(string); image == "" {
			if _, hasBuild := svc["build"]; !hasBuild {
				return nil, fmt.Errorf("services.%s needs an image", name)
			}
		}

		volumes, _ := svc["volumes"].([]interface{})
		for i, v := range volumes {
			source, isBind := "", false
			switch vol := v.(type) {
			case string:
				// short syntax: source:target[:mode]; a bind source is a path
				parts := strings.SplitN(vol, ":", 2)
				if len(parts) == 2 && (strings.HasPrefix(parts[0], "/") || strings.HasPrefix(parts[0], ".")) {
					source, isBind = parts[0], true
				}
			case map[string]interface{}:
				if t, _ := vol["type"].(string); t == "bind" {
					source, _ = vol["source"].(string)
					isBind = true
				}
			default:
				return nil, fmt.Errorf("services.%s.volumes[%d] has an unsupported form", name, i)
			}
			if isBind && !strings.HasPrefix(source, "/mnt/") {
				return nil, fmt.Errorf("services.%s.volumes[%d]: bind mount source %q must be under /mnt/", name, i, source)
			}
		}

		if p, _ := svc["privileged"].(bool); p {
			warnings = append(warnings, fmt.Sprintf("services.%s runs privileged", name))
		}
		if nm, _ := svc["network_mode"].(string); nm == "host" {
			warnings = append(warnings, fmt.Sprintf("services.%s uses host networking", name))
		}
		if caps, _ := svc["cap_add"].([]interface{}); len(caps) > 0 {
			warnings = append(warnings, fmt.Sprintf("services.%s adds capabilities %v", name, caps))
		}
		if devs, _ := svc["devices"].([]interface{}); len(devs) > 0 {
			warnings = append(warnings, fmt.Sprintf("services.%s maps host devices %v", name, devs))
		}
	}
	sort.Strings(warnings)
	return warnings, nil
}

func parseCustomAppArgs(args map[string]interface{}) (string, map[string]interface{}, bool, error) {
	appName, ok := args["app_name"].(string)
	if !ok || appName == "" {
		return "", nil, false, fmt.Errorf("app_name is required")
	}
	if err := validateAppName(appName); err != nil {
		return "", nil, false, fmt.Errorf("invalid app_name: %v", err)
	}
	compose, ok := args["compose"].(map[string]interface{})
	if !ok {
		return "", nil, false, fmt.Errorf("compose is required and must be an object (the compose file's JSON form)")
	}
	update, _ := args["update"].(bool)
	return appName, compose, update, nil
}

func (r *Registry) handleDeployCustomApp(client *truenas.Client, args map[string]interface{}) (string, error) {
	appName, compose, update, err := parseCustomAppArgs(args)
	if err != nil {
		return "", err
	}
	warnings, err := validateCustomCompose(compose)
	if err != nil {
		return "", fmt.Errorf("compose validation failed: %v", err)
	}

	method := "app.create"
	var result json.RawMessage
	if update {
		method = "app.update"
		result, err = client.Call("app.update", appName, map[string]interface{}{
			"custom_compose_config": compose,
		})
	} else {
		result, err = client.Call("app.create", map[string]interface{}{
			"app_name":              appName,
			"custom_app":            true,
			"custom_compose_config": compose,
		})
	}
	if err != nil {
		return "", fmt.Errorf("%s failed: %w", method, err)
	}

	var jobID int
	if err := json.Unmarshal(result, &jobID); err != nil {
		var arr []int
		if err2 := json.Unmarshal(result, &arr); err2 != nil || len(arr) == 0 {
			return "", fmt.Errorf("failed to parse job ID from %s: %s", method, string(result))
		}
		jobID = arr[0]
	}

	task, err := r.taskManager.CreateJobTask("deploy_custom_app", args, jobID, 1*time.Hour)
	if err != nil {
		return "", fmt.Errorf("failed to create task: %w", err)
	}
	response := map[string]interface{}{
		"app_name":      appName,
		"method":        method,
		"job_id":        jobID,
		"task_id":       task.TaskID,
		"task_status":   task.Status,
		"poll_interval": task.PollInterval,
		"message":       fmt.Sprintf("Custom app %s started. Track progress with tasks_get using task_id: %s", method, task.TaskID),
	}
	if len(warnings) > 0 {
		response["warnings"] = warnings
	}
	formatted, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return "", err
	}
	return string(formatted), nil
}

func (r *Registry) handleDeployCustomAppWithDryRun(client *truenas.Client, args map[string]interface{}) (string, error) {
	return ExecuteWithDryRun(client, args, &deployCustomAppDryRun{}, r.handleDeployCustomApp)
}

type deployCustomAppDryRun struct{}

func (d *deployCustomAppDryRun) ExecuteDryRun(client *truenas.Client, args map[string]interface{}) (*DryRunResult, error) {
	appName, compose, update, err := parseCustomAppArgs(args)
	if err != nil {
		return nil, err
	}
	warnings, err := validateCustomCompose(compose)
	if err != nil {
		return nil, fmt.Errorf("compose validation failed: %v", err)
	}

	services := compose["services"].(map[string]interface{})
	names := make([]string, 0, len(services))
	images := map[string]interface{}{}
	for name, raw := range services {
		names = append(names, name)
		if svc, ok := raw.(map[string]interface{}); ok {
			images[name] = svc["image"]
		}
	}
	sort.Strings(names)

	op, target, desc := "create", "app.create", fmt.Sprintf("Create custom app %s", appName)
	if update {
		op, target, desc = "update", "app.update", fmt.Sprintf("Replace compose config of custom app %s (containers are recreated)", appName)
	}
	return &DryRunResult{
		Tool:         "deploy_custom_app",
		CurrentState: map[string]interface{}{"app_name": appName, "services": names, "images": images},
		PlannedActions: []PlannedAction{{
			Step: 1, Description: desc, Operation: op, Target: target,
		}},
		Warnings:      warnings,
		EstimatedTime: &EstimatedTime{MinSeconds: 10, MaxSeconds: 300, Note: "Depends on image pull time"},
	}, nil
}
