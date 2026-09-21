package cursor

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"

	sdkv1 "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/sdk/v1"
)

// modelInfoFromCatalog maps one SDK catalog entry onto the registry model info the host's
// model registry consumes. Exclusions and aliases are applied by the caller through the
// native registry path, not here.
func modelInfoFromCatalog(model *sdkv1.SdkModel) (*registry.ModelInfo, bool) {
	id := strings.TrimSpace(model.GetId())
	if id == "" {
		return nil, false
	}
	displayName := strings.TrimSpace(model.GetDisplayName())
	if displayName == "" {
		displayName = id
	}
	parameters := make([]string, 0, len(model.GetParameters()))
	for _, parameter := range model.GetParameters() {
		if parameterID := strings.TrimSpace(parameter.GetId()); parameterID != "" {
			parameters = append(parameters, parameterID)
		}
	}
	return &registry.ModelInfo{
		ID:                         id,
		Object:                     "model",
		OwnedBy:                    providerIdentifier,
		Type:                       "chat",
		DisplayName:                displayName,
		Name:                       id,
		Description:                strings.TrimSpace(model.GetDescription()),
		SupportedGenerationMethods: []string{"chat"},
		SupportedInputModalities:   []string{"text", "image"},
		SupportedOutputModalities:  []string{"text"},
		SupportedParameters:        parameters,
	}, true
}
