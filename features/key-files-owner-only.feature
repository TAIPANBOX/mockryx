# A key file is read only when its owner rule holds: PostgreSQL's rule for the private
# key a server loads. Each scenario is bound to the test that proves it, by the
# @bound: tag directly above it; scripts/features-are-bound.sh holds the binding in
# both directions. The owner cases run with an injected uid and stat, so they need no
# privilege.
#
# @decided 2026-10-17: a key file is refused unless it is owned by this user at 0600
# or stricter, or by root at 0640 or stricter. On Windows the owner rule is not made.
Feature: A key file is read only when its owner rule holds
  The client key and the x-api-key are credentials. A key file is read only when it
  is owned by this user at 0600 or stricter, or by root at 0640 or stricter, which is
  the rule PostgreSQL applies to its private key. The root case admits the group read
  a Kubernetes Secret presents to a non-root process under an fsGroup. On Windows the
  permission bits do not mean what they mean elsewhere, so the owner rule is not made
  there, and the file must still be a regular file.

  @bound:TestOwnerRuleOwnedByThisUserAt0600IsAccepted
  Scenario: A key file owned by this user at 0600 is read
    Given a key file owned by this user with mode 0600 or 0400
    When the drill reads it
    Then the key is returned

  @bound:TestOwnerRuleOwnedByThisUserAt0640IsRefused
  Scenario: A key file owned by this user with a group bit is refused
    Given a key file owned by this user with mode 0640
    When the drill reads it
    Then the read is refused, and the error names the file, its mode and the fix

  @bound:TestOwnerRuleOwnedByRootAt0440IsAccepted
  Scenario: A key file owned by root with group read only is read
    Given a key file owned by root with mode 0440, as a Kubernetes Secret presents it under an fsGroup
    When the drill, running as another user, reads it
    Then the key is returned

  @bound:TestOwnerRuleOwnedByRootAt0640IsAccepted
  Scenario: A key file owned by root with group read is read
    Given a key file owned by root with mode 0640
    When the drill, running as another user, reads it
    Then the key is returned

  @bound:TestOwnerRuleOwnedByRootAt0660IsRefused
  Scenario: A key file owned by root with group write is refused
    Given a key file owned by root with mode 0660
    When the drill, running as another user, reads it
    Then the read is refused

  @bound:TestOwnerRuleOwnedByAnotherUserIsRefused
  Scenario: A key file owned by another user is refused, whatever its mode
    Given a key file owned by a user who is neither this user nor root, with mode 0600 or 0400
    When the drill reads it
    Then the read is refused, and the error names the owner

  @bound:TestOwnerRuleAnyOtherReadBitIsRefused
  Scenario: A key file with any access bit for other users is refused
    Given key files with mode 0604, 0644, 0606 or 0445, owned by this user or by root
    When the drill reads them
    Then every read is refused

  @bound:TestOwnerRuleAsRootTheOwnerRuleComesFirst
  Scenario: A process running as root judges a root-owned file as its own
    Given a process running as root and a key file owned by root with mode 0640
    When the drill reads it
    Then the read is refused, because the file is judged as owned by this user, which needs 0600

  @bound:TestReadKeyFileJudgesTheTargetOfASymlink
  Scenario: A link to a key file is judged by the file it leads to
    Given a symlink to a key file with mode 0600, and a symlink to one with mode 0640
    When the drill reads each link
    Then the first is read and the second is refused

  @bound:TestRunCommandRefusesAClientKeyFileGroupOrOtherCanRead
  Scenario: A client key file that other users can read is refused before any request
    Given a client key file with mode 0644
    When the drill runs with --client-key-file naming that file
    Then it exits with a usage error that names the file and its mode, and the gateway receives no request

  @bound:TestReadAPIKeyFileRefusesAFileGroupOrOtherCanRead
  Scenario: An API key file with a group bit, owned by this user, is refused and its key is not printed
    Given an API key file owned by this user with mode 0640 holding a key
    When the drill reads the API key file
    Then the read is refused, the error names the file and its mode, and it does not print the key

  @bound:TestReadKeyFileRefusesANonRegularFile
  Scenario: A key path that is not a regular file is refused
    Given a directory where a key file was expected
    When the drill reads it as either key file
    Then the read is refused and the error says the path is not a regular file

  @bound:TestReadKeyFileAcceptsAnOwnerOnlyFile
  Scenario: An owner-only key file is read
    Given a key file owned by this user with mode 0600 or 0400 holding one key
    When the drill reads it as either key file
    Then the key is returned

  @bound:TestKeyFileModeCheckIsSkippedOnWindowsByInjection
  Scenario: On Windows the owner rule is not made, and the file is still read
    Given a key file with mode 0644, and the drill is told it runs on Windows
    When the drill reads it as the API key file
    Then the key is read, and on Linux the same file is refused
