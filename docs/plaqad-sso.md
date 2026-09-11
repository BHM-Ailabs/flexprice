# Plaqad BSP operator access

`FLEXPRICE_AUTH_PLAQAD_ENABLED=true` replaces native human login with Plaqad SSO.
Machine API keys retain their existing tenant and environment permissions.

Set `FLEXPRICE_AUTH_PLAQAD_ALLOWED_USER_IDS` to comma-separated, exact central
user IDs to restrict BSP access to approved operators. IDs are case-sensitive;
spaces around configured entries are ignored. Both the code-exchange callback
and each live Super Admin check enforce this list. An omitted or empty list
preserves the existing behavior of admitting every central Super Admin. A
nonempty list containing only blank entries admits nobody. The allowlist never
grants the Super Admin role itself.

The mapped `FLEXPRICE_AUTH_PLAQAD_USER_ID` must still be a published native
`super_admin` in `FLEXPRICE_AUTH_PLAQAD_TENANT_ID`. Keep that ID as the native
data-access principal. The verified human's central ID is recorded separately as
`plaqad_user_id` in request-scoped logs and `app.plaqad_user_id` in tracing.
Existing native `created_by`/`updated_by` fields retain the native principal.
Plaqad Auth's invoice and pricing APIs receive the human JWT directly and own
their own actor audit and MFA rules.

For rollout, verify the saved service secret, exact callback/origin, operator
allowlist and native mapping before enabling SSO. Apply the allowlist in the same
configuration change as the enabled flag; never enable broadly as an intermediate
step. Deploy the allowlist-capable API before enabling the SSO frontend. Keep a
private variable snapshot and previous source/image pins for rollback. Restoring
an old image alone does not disable newly configured SSO variables.

Relevant tests cover environment decoding, absent-list compatibility, exact-ID
matching, unrelated Super Admin rejection, blank configuration, callback token
withholding, mapped-role revocation, native-token rejection, API-key continuity,
Auth outage behavior, and separate native/central log attribution.
