# A key file is read only when only its owner can read it, because the key in it
# is what other users would otherwise see. Each scenario is bound to the test that
# proves it, by the @bound: tag directly above it; scripts/features-are-bound.sh
# holds the binding in both directions.
#
# @decided 2026-10-17: a key file that group or other users can read is refused,
# before any request, for the client key and the x-api-key alike. On Windows the
# mode bits do not mean what they mean elsewhere, so the mode rule is not applied
# there: a named limit, stated in the docs.
Feature: A key file is read only when its owner alone can read it
  The client key and the x-api-key are credentials. A key file other users can
  read hands the key to them, so the drill refuses it before it sends anything.
  The same rule covers both key files. On Windows the permission bits cannot tell
  an owner-only file from an ordinary one, so the mode rule is skipped there, and
  the file must still be a regular file.

  @bound:TestRunCommandRefusesAClientKeyFileGroupOrOtherCanRead
  Scenario: A client key file that other users can read is refused before any request
    Given a client key file with mode 0644
    When the drill runs with --client-key-file naming that file
    Then it exits with a usage error that names the file and its mode, and the gateway receives no request

  @bound:TestReadAPIKeyFileRefusesAFileGroupOrOtherCanRead
  Scenario: An API key file the group can read is refused, and its key is not printed
    Given an API key file with mode 0640 holding a key
    When the drill reads the API key file
    Then the read is refused, the error names the file and its mode, and it does not print the key

  @bound:TestReadKeyFileRefusesANonRegularFile
  Scenario: A key path that is not a regular file is refused
    Given a directory where a key file was expected
    When the drill reads it as either key file
    Then the read is refused and the error says the path is not a regular file

  @bound:TestReadKeyFileAcceptsAnOwnerOnlyFile
  Scenario: An owner-only key file is read
    Given a key file with mode 0600 or 0400 holding one key
    When the drill reads it as either key file
    Then the key is returned

  @bound:TestKeyFileModeCheckIsSkippedOnWindowsByInjection
  Scenario: On Windows the mode is not checked, and the file is still read
    Given a key file with mode 0644, and the drill is told it runs on Windows
    When the drill reads it as the API key file
    Then the key is read, and on Linux the same file is refused
