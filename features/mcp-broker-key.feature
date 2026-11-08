# A broker that sets TOKENFUSE_MCP_KEYS refuses a call with no key at its door.
# These scenarios say what the drill does about that without handing the broker
# the gateway's key. Each scenario is bound to the test that proves it, by the
# @bound: tag directly above it; scripts/features-are-bound.sh holds the binding
# in both directions.
#
# @decided 2026-11-08: the drill takes a key for the MCP broker that is separate
# from the gateway's client key, so the gateway's key still never reaches the
# broker.
Feature: A drill presents the MCP broker's own key to the broker alone
  The broker has a door of its own. The key it admits is not the gateway's, and
  neither service is ever shown the other's key.

  @bound:TestMCPStepSendsTheBrokerKeyAsXFuseKey
  Scenario: An MCP step carries the broker key
    Given the operator has given the drill a broker key
    When the drill sends an MCP step to the broker
    Then the broker receives the key in the x-fuse-key header

  @bound:TestBrokerKeyGoesToTheBrokerOnly
  Scenario: Each service receives its own key and never the other's
    Given the operator has given the drill a client key and a broker key
    When the drill sends a messages step, a tool check and an MCP step
    Then the gateway receives the client key and the broker receives the broker key

  @bound:TestClientKeyStillNeverReachesTheBrokerWithABrokerKeySet
  Scenario: The gateway key never reaches the broker, whichever keys are set
    Given any mix of a client key and a broker key, or neither
    When the drill sends an MCP step to the broker
    Then the broker receives the broker key or nothing, and never the client key

  @bound:TestBrokerKeyNeverReachesTheGateway
  Scenario: The broker key never reaches the gateway
    Given the operator has given the drill only a broker key
    When the drill sends a messages step and a tool check to the gateway
    Then the gateway receives no x-fuse-key header

  @bound:TestRunCommandWithNoBrokerKeyBehavesAsItDid
  Scenario: With no broker key configured the broker is sent none
    Given the operator has given the drill no broker key
    When the drill runs against a broker that has a door
    Then the broker receives no key and the refusal is still a finding

  @bound:TestRunCommandRefusesTheSameKeyForTheGatewayAndTheBroker
  Scenario: One secret given as both keys is refused
    Given the same secret given as the client key and as the broker key
    When the operator runs a drill
    Then the run exits as a usage error and nothing is sent

  @bound:TestRunCommandHasNoFlagThatTakesTheBrokerKeyValue
  Scenario: The broker key value cannot be passed on the command line
    Given the operator tries to pass the broker key value as a flag
    When the operator runs a drill
    Then the run exits as a usage error, so the key never sits in the process arguments

  @bound:TestRunCommandRefusesTwoBrokerKeySourcesBeforeSendingAnything
  Scenario: A broker key given both as a file and as a value is refused
    Given a key file and the key value both given
    When the operator runs a drill
    Then the run exits as a usage error and nothing is sent

  @bound:TestFindingsNeverCarryTheBrokerKey
  Scenario: The broker key never appears in a finding
    Given a broker that echoes the key back in a reply and a header
    When the drill runs a scenario that does not match
    Then no part of the serialised result contains the key

  @bound:TestRunCommandBrokerKeyNeverReachesTheReportOrTheEvents
  Scenario: The broker key never reaches the report, the saved file or the events
    Given a broker that echoes the key back
    When the operator runs a drill with a report, a saved file and an event stream
    Then none of the three contains the key
