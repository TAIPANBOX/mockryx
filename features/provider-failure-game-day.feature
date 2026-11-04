# A game day for a provider that is up but failing: it answers with a 5xx, a 429
# or a timeout. The drill sends the operator's own gateway a call and checks the
# chain that follows: the agent's call comes back with the provider's own
# status, the gateway records a dependency_failed for that run, and the run
# reaches a person. The provider is a local stub and nothing else. Each scenario
# is bound to the test that proves it, by the @bound: tag directly above it;
# scripts/features-are-bound.sh holds the binding in both directions.
#
# @decided 2026-11-04: the failing provider is always a local stub that listens
# on loopback and makes no outbound call, so a game day can never reach a real
# provider; the drills use only the existing event check and add no scenario
# capability.
Feature: A drill rehearses what the chain does when the provider fails
  The gateway is pointed at a stub provider that fails on purpose. The drill is
  clean when the call degrades with the provider's status, the bus carries the
  failure and a person is told, and it is a finding when any link is missing.

  @bound:TestProviderDrillsPassWhenTheWholeChainHolds
  Scenario: The whole chain holds for each way a provider fails
    Given a gateway whose provider answers 503, 429 or 504
    And the gateway records a dependency_failed and a person is told
    When the drill runs
    Then the drill passes

  @bound:TestProviderDrillsExpectTheStatusTheirStubAnswers
  Scenario: Each drill expects the status its stub answers
    Given the three provider failure drills and the stub modes they name
    When the expected statuses are read from the drills
    Then each one matches the status of its stub

  @bound:TestProviderDrillsFailWhenTheGatewayHidesTheFailure
  Scenario: A gateway that hides the failing provider fails the drill
    Given a gateway that answers 200 whatever the provider said
    When the drill runs
    Then the drill fails on the status the agent received

  @bound:TestProviderDrillsNameTheMissingEventWhenTheBusStaysSilent
  Scenario: A bus with no dependency_failed fails the drill and says which event
    Given a gateway that passes the provider's status through but records nothing
    When the drill runs
    Then the drill fails with a finding that names tokenfuse and dependency_failed

  @bound:TestProviderDrillsNameTheMissingNoticeWhenNobodyIsTold
  Scenario: A failure that reaches the bus but not a person fails the drill and says which notice
    Given a gateway that records the failure and a notifier that stays silent
    When the drill runs
    Then the drill fails with a finding that names heraldyx and alert_sent

  @bound:TestProviderDrillsAreNeverSkippedAsNotConfigured
  Scenario: A provider failure drill cannot be skipped as not configured
    Given a failing provider sends no header a drill could read
    When the drills are loaded
    Then none declares a required feature, so a miss is always a finding

  @bound:TestStubAnswers503WhenTheProviderIsDown
  Scenario: The stub answers 503 when it plays a provider that is down
    Given the stub in 5xx mode
    When a request reaches it
    Then it answers 503

  @bound:TestStubAnswers429WithARetryAfterWhenRateLimited
  Scenario: The stub answers 429 with a Retry-After when it plays a throttling provider
    Given the stub in 429 mode
    When a request reaches it
    Then it answers 429 and a Retry-After

  @bound:TestStubHoldsTheRequestThenAnswers504WhenTheProviderTimesOut
  Scenario: The stub holds a request and then answers 504 when it plays a provider that times out
    Given the stub in timeout mode with a short hold
    When a request reaches it
    Then it answers 504 only after the hold

  @bound:TestListenRefusesEveryAddressThatIsNotLoopback
  Scenario: The stub refuses to listen anywhere but loopback
    Given a listen address that is a LAN address, a wildcard or a public name
    When the stub is asked to listen on it
    Then it refuses before binding anything

  @bound:TestProviderStubExitsTwoOnAnAddressThatIsNotLoopback
  Scenario: The stub command exits as a usage error on an address that is not loopback
    Given the stub's command and a wildcard or LAN address
    When it starts
    Then it exits 2 and says the address must be loopback

  @bound:TestNoOutboundCallExistsInTheStub
  Scenario: The stub makes no outbound call
    Given the stub's source
    When it is scanned for any way to reach another host
    Then none is found, and the scanner is shown to see a planted one
