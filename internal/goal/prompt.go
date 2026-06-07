package goal

import "fmt"

func StartPrompt(state State) string {
	return fmt.Sprintf(`<system-reminder>
An explicit session goal has been created by the user.

<objective>
%s
</objective>

%s

Start working toward this goal now. Use get_goal when you need the persisted goal state. Keep the full objective intact; do not redefine success around a smaller, safer, or easier-to-test task. When you think the goal is finished, audit each requirement against current evidence before calling update_goal with status "complete".

Do not call update_goal with status "blocked" the first time you encounter a blocker. The same blocking condition must recur for at least three consecutive goal turns before blocked is valid.
</system-reminder>`, state.Objective, budgetSummary(state))
}

func ContinuationPrompt(state State) string {
	return fmt.Sprintf(`<system-reminder>
Continue working toward the active explicit session goal.

<objective>
%s
</objective>

%s

Work from current evidence. Inspect the current files, tool outputs, tests, and external state that are authoritative for this objective; do not rely on memory or earlier intent when evidence may have changed.

Preserve the original scope. Do not substitute a narrower, simpler, safer, or merely test-passing version of the user's requested end state. Rough edges are acceptable while you are still moving, but completion requires the requested state to be true and verified.

Completion audit:
- Derive the concrete requirements from the objective, user instructions, files, plans, specs, issues, tests, and acceptance criteria.
- For every required artifact, command, test, invariant, and deliverable, identify the authoritative evidence and inspect it.
- Treat missing, indirect, stale, or uncertain evidence as incomplete.
- Call update_goal with status "complete" only when the current evidence proves every requirement is satisfied and no required work remains.

Blocked audit:
- Do not call update_goal with status "blocked" on the first occurrence of a blocker.
- The same blocking condition must recur for at least three consecutive goal turns, counting the original user-triggered turn and automatic continuations.
- Do not mark blocked merely because the work is hard, slow, uncertain, incomplete, or would benefit from clarification.
- Once the threshold is satisfied and you are at a true impasse, call update_goal with status "blocked" and provide the blocker.

Do not stop merely because the work is long, difficult, or would benefit from more time.
</system-reminder>`, state.Objective, budgetSummary(state))
}

func BudgetLimitPrompt(state State) string {
	return fmt.Sprintf(`<system-reminder>
The active explicit session goal has reached its token budget.

<objective>
%s
</objective>

%s

Do not start new substantive work. Wrap up the current state clearly: what was completed, what remains, and the next concrete step. Do not call update_goal with status "complete" unless the completion audit is actually satisfied by current evidence.
</system-reminder>`, state.Objective, budgetSummary(state))
}

func budgetSummary(state State) string {
	if state.TokenBudget <= 0 {
		return fmt.Sprintf("Tokens used: %d\nToken budget: none\nTokens remaining: unbounded", state.TokensUsed)
	}
	remaining := state.TokenBudget - state.TokensUsed
	if remaining < 0 {
		remaining = 0
	}
	return fmt.Sprintf("Tokens used: %d\nToken budget: %d\nTokens remaining: %d", state.TokensUsed, state.TokenBudget, remaining)
}
