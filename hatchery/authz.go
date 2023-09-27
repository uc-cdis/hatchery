package hatchery

import (
	"fmt"
)

type AuthzConfig struct {
	Version float32          `json:"version"`
	Rules   AuthzVersion_0_1 `json:"rules"`
}

type AuthzVersion_0_1 struct {
	And           []AuthzVersion_0_1 `json:"and"`
	Or            []AuthzVersion_0_1 `json:"or"`
	ResourcePaths []string           `json:"resource_paths"`
	PayModels     []string           `json:"pay_models"`
}

func ValidateAuthzConfig(authzConfig AuthzConfig) error {
	if authzConfig.Version != 0.1 {
		return fmt.Errorf("Container authz config version '%v' is not valid", authzConfig.Version)
	}

	// check that only 1 of and/or/resource_paths/pay_models is set in the same block.
	// NOTE: if we implement support for nested rules, we should validate each nested level this way
	isOrStmt, isAndStmt, isResourcePathsStmt, isPayModelsStmt := 0, 0, 0, 0
	if len(authzConfig.Rules.Or) > 0 {
		isOrStmt = 1
	}
	if len(authzConfig.Rules.And) > 0 {
		isAndStmt = 1
	}
	if len(authzConfig.Rules.ResourcePaths) > 0 {
		isResourcePathsStmt = 1
	}
	if len(authzConfig.Rules.PayModels) > 0 {
		isPayModelsStmt = 1
	}
	sum := isOrStmt + isAndStmt + isResourcePathsStmt + isPayModelsStmt
	if sum != 1 {
		return fmt.Errorf("there should be exactly 1 key with non-null value on the 1st level of authz config, found %d", sum)
	}

	// although the `AuthzVersion_0_1` struct allows it, nesting and/or rules is not supported yet
	if isOrStmt == 1 || isAndStmt == 1 {
		for _, rule := range append(authzConfig.Rules.Or, authzConfig.Rules.And...) {
			if len(rule.Or) > 0 || len(rule.And) > 0 {
				return fmt.Errorf("nesting 'and' and 'or' authorization rules is not supported")
			}
		}
	}

	return nil
}
