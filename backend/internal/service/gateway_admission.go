package service

import "context"

// gatewayGroupAllowsClient only checks admission. The caller decides whether a
// rejection may fall back to another group or must preserve a permanent binding.
func gatewayGroupAllowsClient(ctx context.Context, group *Group, forcedPlatform bool) bool {
	return group == nil || forcedPlatform || !group.ClaudeCodeOnly || IsClaudeCodeClient(ctx)
}

// gatewayAccountMeetsPrivacyRequirement is shared by candidate selection,
// sticky sessions and permanent bindings. It never changes account state.
func gatewayAccountMeetsPrivacyRequirement(group *Group, account *Account) bool {
	return account != nil && (group == nil || !group.RequirePrivacySet || account.IsPrivacySet())
}
