# Recovering an unverified account

Use this operator procedure only for an enabled, CONFIRMED Cognito user who
forgot the password and has no verified recovery email. The pool is email-only.
Verify the person's identity through the approved support process before changing
credentials; possession of an email address alone is not identity proof.
Do not delete/recreate the user or manually set email_verified.

Export the nonsecret variables TTOBAK_AWS_PROFILE, TTOBAK_REGION,
TTOBAK_USER_POOL_ID and TTOBAK_USER_SUB from the verified deployment and user
record. Use the immutable Cognito sub. In a private operator terminal, verify the
STS account/assumed role and the selected user's status; stop on any mismatch:

```bash
aws sts get-caller-identity --profile "$TTOBAK_AWS_PROFILE" --region "$TTOBAK_REGION"
aws cognito-idp admin-get-user \
  --profile "$TTOBAK_AWS_PROFILE" --region "$TTOBAK_REGION" \
  --user-pool-id "$TTOBAK_USER_POOL_ID" --username "$TTOBAK_USER_SUB" \
  --query '{Enabled:Enabled,Status:UserStatus,Attributes:UserAttributes}'
```

After explicitly approving this account's recovery, set a temporary password.
The hidden prompt keeps it out of shell history and process arguments; the JSON
goes directly to the AWS CLI pipe. Do not enable shell tracing, debug logging or
CI execution for this procedure. This operation does not send an invitation:

```bash
set +x
set -o pipefail
python3 -c 'import getpass,json,os; print(json.dumps({
    "UserPoolId": os.environ["TTOBAK_USER_POOL_ID"],
    "Username": os.environ["TTOBAK_USER_SUB"],
    "Password": getpass.getpass("Temporary password: "),
    "Permanent": False
}))' | aws cognito-idp admin-set-user-password \
  --profile "$TTOBAK_AWS_PROFILE" --region "$TTOBAK_REGION" \
  --cli-input-json file:///dev/stdin
```

Provide the temporary password through the approved private support channel.
The user signs in and completes the existing NEW_PASSWORD_REQUIRED screen, then
verifies the email through the authenticated application controls. Recovery is
complete only after a fresh token has the verified claim and pending grants can
be retried. A failed claim refresh requires sign-in again, not a false success.
Never record passwords or verification codes in a PR, incident log or screenshot.
