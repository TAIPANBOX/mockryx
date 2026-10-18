# The gateway refuses a call with no client key (x-fuse-key), and a key speaks
# only as the agent ids it is bound to. These scenarios say what the drill does
# about that. Each scenario is bound to the test that proves it, by the
# @bound: tag directly above it; scripts/features-are-bound.sh holds the binding
# in both directions.
#
# @decided 2026-10-17: a drill presents the operator's key and an agent id under
# the key's namespace, and a refusal of that credential is never read as a
# guardrail result.
Feature: A drill presents the operator's client key and agent id
  The gateway holds a key per caller, and each key may speak only as the agent
  ids it is bound to. A drill that does not present one is refused before any
  guardrail is asked, so that refusal must never pass for a guardrail's answer.

  @bound:TestMessagesStepSendsClientKeyAsXFuseKey
  Scenario: A messages step carries the client key
    Given the operator has given the drill a client key
    When the drill sends a messages step to the gateway
    Then the gateway receives the key in the x-fuse-key header

  @bound:TestToolCheckStepSendsClientKeyAsXFuseKey
  Scenario: A tool check carries the client key
    Given the operator has given the drill a client key
    When the drill asks the gateway about a tool call
    Then the gateway receives the key in the x-fuse-key header

  @bound:TestMCPStepNeverSendsClientKey
  Scenario: The MCP broker never receives the gateway key
    Given the operator has given the drill a client key
    When the drill sends an MCP step to the broker
    Then the broker receives no x-fuse-key header

  @bound:TestNoClientKeyConfiguredSendsNoXFuseKeyHeader
  Scenario: With no key configured the drill sends no key header
    Given the operator has given the drill no client key
    When the drill sends a messages step to the gateway
    Then the gateway receives no x-fuse-key header

  @bound:TestAgentIDOverrideReplacesTheScenarioAgentIDOnEveryStepKind
  Scenario: The configured agent id replaces the scenario's own on every step kind
    Given the operator has given the drill an agent id under the key's namespace
    When the drill runs a scenario with a messages, a tool check and an MCP step
    Then every step sends the configured agent id and none sends the scenario's own

  @bound:TestAgentIDWithoutOverrideKeepsTheScenarioAgentID
  Scenario: Without an agent id override the scenario's own id is sent
    Given the operator has given the drill no agent id
    When the drill sends a messages step to the gateway
    Then the gateway receives the scenario's own agent id

  @bound:TestUnauthorizedGatewayReplyIsRefusedCredentialNotGuardrail
  Scenario: A refused key is reported as a refused credential, not as a guardrail result
    Given a gateway that answers 401 to every call
    When the drill runs a scenario that declares a guardrail
    Then the scenario fails with a finding that says the gateway refused the drill's credential, and it is not skipped as not configured

  @bound:TestIdentityMismatchReplyIsRefusedCredentialNotGuardrail
  Scenario: An agent id outside the key's namespace is reported as a refused credential
    Given a gateway that answers 403 identity_mismatch
    When the drill runs a scenario that expects a 403 from a guardrail
    Then the scenario fails with a finding that says the gateway refused the drill's credential, and the 403 does not pass as the guardrail's answer

  @bound:TestRefusedCredentialStopsTheScenario
  Scenario: A refused credential ends the scenario at its first refusal
    Given a gateway that answers 401 to every call
    When the drill runs a scenario with two messages steps
    Then the gateway receives one request and the scenario reports one finding

  @bound:TestPlainForbiddenWithNoSignalStaysASkip
  Scenario: An ordinary 403 with no guardrail signal is still a skip, not a refused credential
    Given a gateway that answers a plain 403 with no guardrail header
    When the drill runs a scenario that declares a guardrail
    Then the scenario is skipped as not configured

  @bound:TestFindingsNeverCarryTheClientKey
  Scenario: The client key never appears in a finding
    Given a gateway that echoes the key back in a reply and a header
    When the drill runs a scenario that does not match
    Then no part of the serialised result contains the key

  @bound:TestReadClientKeyFileReturnsTheKeyWithoutTheTrailingNewline
  Scenario: A key file yields the key without its trailing newline
    Given a key file holding one key and a newline
    When the drill reads the file
    Then the drill has the key alone

  @bound:TestReadClientKeyFileRefusesAFileThatIsNotOneKey
  Scenario: A key file that is empty or holds more than one value is refused
    Given a key file that is empty, or holds two words
    When the drill reads the file
    Then the drill refuses it, and the error does not repeat the file's contents

  @bound:TestRunCommandRefusesAnUnreadableKeyFileBeforeSendingAnything
  Scenario: An unreadable key file is a usage error before any request is sent
    Given a key file path that does not exist
    When the operator runs a drill against a gateway
    Then the run exits as a usage error and the gateway receives no request

  @bound:TestRunCommandRefusesTwoKeySourcesBeforeSendingAnything
  Scenario: A key given both as a file and as a value is refused
    Given a key file and the key value both given
    When the operator runs a drill against a gateway
    Then the run exits as a usage error and the gateway receives no request

  @bound:TestRunCommandHasNoFlagThatTakesTheKeyValue
  Scenario: The key value cannot be passed on the command line
    Given the operator tries to pass the key value as a flag
    When the operator runs a drill
    Then the run exits as a usage error, so the key never sits in the process arguments
