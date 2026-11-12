# The reaction chain behind one denied tool call: the gateway refuses it in
# path, wardryx writes its own record of the refusal, and heraldyx, reading
# nothing but that record, journals one notification for the run. Two drills
# rehearse it, one per downstream link, and each waits for its reaction in the
# same step that sends the request the reaction is to. Each scenario is bound
# to the test that proves it, by the @bound: tag directly above it;
# scripts/features-are-bound.sh holds the binding in both directions.
Feature: The reaction-chain drills report a gap only when a link of the chain is missing
  The drills send the operator's own gateway the request wardryx-denied-tool
  sends. They are clean when the gateway refuses it, wardryx records the
  refusal and heraldyx journals a notification for the run, and each names the
  link that is missing when one is.

  @bound:TestReactionChainPassesWhenHeraldyxJournalsBeforeTheNextRequest
  Scenario: A heraldyx that journals at once and dedups by run is not a gap
    Given a gateway that refuses the denied tool and answers in the next second
    And wardryx records the refusal and heraldyx journals one alert for the run
    When the heraldyx drill runs
    Then the drill passes

  @bound:TestReactionChainNamesHeraldyxWhenNobodyIsTold
  Scenario: Nobody told about the refusal is a gap that names heraldyx
    Given a gateway that refuses the denied tool
    And heraldyx journals nothing, or wardryx keeps no record for it to read
    When the heraldyx drill runs
    Then the drill fails naming the heraldyx alert_sent event

  @bound:TestReactionChainReachesWardryxPassesWhenWardryxRecordsTheDeny
  Scenario: A wardryx that records its refusal passes the wardryx drill
    Given a gateway that refuses the denied tool
    And wardryx records the refusal for the run
    When the wardryx drill runs
    Then the drill passes

  @bound:TestReactionChainReachesWardryxNamesWardryxWhenItKeepsNoRecord
  Scenario: A refusal wardryx keeps no record of is a gap that names wardryx
    Given a gateway that refuses the denied tool
    And wardryx keeps no record of the refusal
    When the wardryx drill runs
    Then the drill fails naming the wardryx policy_deny event

  @bound:TestReactionChainDrillsAreOneStepEachWithTheDeniedToolRequest
  Scenario: Each drill is one step with the denied-tool request and a run id of its own
    Given the two reaction-chain drills as they ship
    When their steps are read
    Then each has one step that sends the request wardryx-denied-tool sends
    And each leaves the run id to be minted per run and waits for its own link
