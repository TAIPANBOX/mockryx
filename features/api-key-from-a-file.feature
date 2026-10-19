# The x-api-key a drill sends is a credential of the same kind as the client key.
# Each scenario is bound to the test that proves it, by the @bound: tag directly
# above it; scripts/features-are-bound.sh holds the binding in both directions.
#
# @decided 2026-10-17: the drill's x-api-key can be given from a file, and a key
# value on the command line is still accepted but is named with a warning.
Feature: A drill's x-api-key can come from a file, not from the command line
  A value typed after --api-key sits in the process list, where other users on
  the machine can read it. A key file keeps the key off argv. A key given both
  ways is refused rather than guessed between, and a key given on the command
  line is still sent, with a warning that never prints the key.

  @bound:TestRunCommandSendsTheAPIKeyFromAFile
  Scenario: A key file named on the command line supplies the x-api-key
    Given the operator has written the drill's x-api-key to a file
    When the drill runs with --api-key-file naming that file
    Then every request to the gateway carries the key from the file

  @bound:TestRunCommandSendsTheAPIKeyFromTheKeyFileEnvironment
  Scenario: The environment names the key file when no flag does
    Given MOCKRYX_API_KEY_FILE names the file holding the key
    When the drill runs without an API key flag
    Then every request to the gateway carries the key from that file

  @bound:TestRunCommandRefusesAnAPIKeyGivenTwiceBeforeSendingAnything
  Scenario: A key given both as a file and as a value is refused before anything is sent
    Given the drill has a key file and a key value on the command line
    When the drill runs
    Then it exits with a usage error that says the key was given twice, and the gateway receives no request

  @bound:TestReadAPIKeyFileRefusesAFileThatIsNotOneKey
  Scenario: A key file that is empty or holds more than one key is refused without printing its contents
    Given a key file that is empty, or that holds two words on one line
    When the drill reads the key file
    Then the read is refused and the error does not repeat what the file holds

  @bound:TestAPIKeyFlagWarnsAndStillSendsTheKey
  Scenario: A key on the command line is still sent, and named with a warning
    Given --api-key with a key value on the command line
    When the drill runs
    Then the gateway receives the key, and stderr has one line naming --api-key that does not print the key

  @bound:TestAPIKeyFromTheEnvironmentIsNotWarned
  Scenario: A key value from the environment gets no warning
    Given MOCKRYX_API_KEY holds the key value
    When the drill runs
    Then the gateway receives the key and stderr says nothing about it
