# Local communications identity encoding

LocalDispatcher and the Android call Provider now reject malformed UTF-8 in their trusted initialization identities, matching the existing wire codec/server contract. The dispatcher checks actor, target, provider and session; the provider checks target, provider and session. Validation precedes environment/provider-identity/readiness callbacks. The existing 256-byte bound, case sensitivity and whitespace rules are unchanged. No Unicode normalization is performed; valid Chinese, emoji and encoded U+FFFD remain valid.

This does not prove remote Android UID, create actor/session authority, enable registration or open any transport/listener/MCP capability. Real call/audio/SMS remains untested and default-off. Tests use fake command/media adapters and simulated platform identity only.

RED tests reproduced all seven malformed fields, three race iterations each, against the prior products. The identical frozen tests then passed with the fix: 33 focused passes, 696 serviceplugins/protocol/androidcall passes, and native build of server/serviceplugins/protocol/androidcall. No new server Communications run was performed; previous server evidence is historical only. Original failures and source hashes are retained. The rejected invokeForProvider experiment is not included.
