# @decided 2026-11-07 (paraphrased): a tool call of an agent nothing grants is held for a
# person, a model call passes, and only an explicit grant lifts the hold. @claude 2026-11-07:
# how the drills read that is my reading, from wardryx v2.6.0 and TokenFuse v1.23.0's reply.
# Each scenario is bound to the test that proves it, by the @bound: tag directly above it;
# scripts/features-are-bound.sh holds the binding in both directions.
Feature: The drills know the tool hold
  Both stacks hold a tool call nothing grants for a person, and grant the fire-drill
  identities every tool. The drills speak as those identities, so they read as they
  always did on a box with the hold, and one drill rehearses the hold itself.

  @bound:TestToolHoldDrillPassesWhenOnlyTheUngrantedIdIsHeld
  Scenario: An id no grant can name is held and a drill identity is not
    Given a broker with the hold and grant-drills
    When the tool hold drill runs
    Then the id no grant can name is held for a person
    And the same call from a drill identity goes through
    And the drill passes

  @bound:TestToolHoldDrillFailsWhenTheDrillIdentityIsHeldToo
  Scenario: A broker that holds everybody fails the drill
    Given a broker with the hold and no grants
    When the tool hold drill runs
    Then the drill fails on the step of the drill identity

  @bound:TestToolHoldDrillReadsAsNotConfiguredWhenTheHoldIsOff
  Scenario: A broker with the hold off reads as not configured
    Given a broker that forwards every call
    When the tool hold drill runs
    Then the drill reports not configured and no Finding

  @bound:TestToolHoldDrillFailsWhenAnotherGuardrailSpeaks
  Scenario: A refusal that is not this rule fails the drill
    Given a broker that denies the call, or holds it under another rule
    When the tool hold drill runs
    Then the drill fails

  @bound:TestScanDrillsReadAsTheyDidWithTheToolHoldOn
  Scenario: The scan drills read as they did with the hold on
    Given a broker with the hold and grant-drills
    When a scan drill runs
    Then it passes when the scan blocks
    And it reports not configured when the scan is off

  @bound:TestScanDrillsReportAFloorWithNoGrantsAsAFinding
  Scenario: A floor that lost grant-drills shows up in the scan drills
    Given a broker with the hold and no grants, and its scans off
    When a scan drill runs
    Then it reports a Finding that quotes when_no_grant

  @bound:TestAFindingQuotesTheWholeSentenceOfARefusal
  Scenario: A report keeps the sentence that says why
    Given a refusal from the broker with its approval id before the reason
    When a Finding quotes it
    Then the reason is all there
    And a reply far longer than any refusal is still cut

  @bound:TestEveryShippedDrillSpeaksUnderTheDrillGrant
  Scenario: Every shipped drill speaks as an identity grant-drills reaches
    Given every scenario that ships
    When their agent ids are read
    Then each sits under agent://mockryx.local/
    And the one step outside it names a domain no grant can name
