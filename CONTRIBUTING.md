# Contributing to HomeShell

Thanks for your interest in HomeShell, a control plane for AI-assisted development
maintained by the Eclipse Enclave project.

- Project home: https://projects.eclipse.org/projects/ecd.enclave
- Developer resources: https://projects.eclipse.org/projects/ecd.enclave/developer
- Source code: https://github.com/eclipse-enclave/homeshell

## Terms of Use

This repository is subject to the [Eclipse Foundation Terms of
Use](https://www.eclipse.org/legal/terms-of-use/).

## Project License

HomeShell is distributed under the [MIT License](LICENSE).
Contributions are received under the terms of that license.

## Contributing Code

1. Before starting substantial work, open or identify a GitHub issue and discuss
   the intended change with the project team.
2. Submit changes as GitHub pull requests against `main`, using the repository's PR template.
3. Keep changes focused and follow the repository guidance in [AGENTS.md](AGENTS.md).
   The [developer guide](docs/DEV.md) covers setup, build and test commands.
4. Format modified Go files and run the required checks before submitting a pull request:

   ```sh
   make build
   make test
   make test-e2e
   make lint
   ```

5. Contributors can use whatever tools they would like to craft their contributions,
   but there must be a human in the loop. Contributors must read and review all
   LLM-generated code or text before they ask other project members to review it.
   The contributor is always the author and is fully accountable for their contributions.
   Contributors should be sufficiently confident that the contribution is high enough
   quality that asking for a review is a good use of scarce maintainer time, and they
   should be able to answer questions about their work themselves during review.

## Eclipse Foundation Development Process

This Eclipse Foundation open source project is governed by the
[Eclipse Foundation Development Process](https://www.eclipse.org/projects/dev_process/)
and operates under the [Eclipse Foundation Intellectual Property
Policy](https://www.eclipse.org/org/documents/Eclipse_IP_Policy.pdf).

## Eclipse Contributor Agreement

Contributors must electronically sign the [Eclipse Contributor Agreement
(ECA)](https://www.eclipse.org/legal/eca/).

The ECA records that every contribution complies with the Developer Certificate
of Origin. The email address in a contribution's Git `Author` field must match
the email address associated with the contributor's ECA.

## Community and Security

Participation follows the [Code of Conduct](CODE_OF_CONDUCT.md).
Report vulnerabilities privately as described in [SECURITY.md](SECURITY.md), not in public issues or PRs.

## Contact

Contact the project developers through the
[enclave-dev mailing list](https://accounts.eclipse.org/mailing-list/enclave-dev).
