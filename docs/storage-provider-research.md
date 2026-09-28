# Screenshot storage provider comparison

Checked against primary documentation on 2026-09-27. Decision updated 2026-09-28:
**Cloudflare R2 selected by the project owner.** R2 configuration and a Docker
overlay are implemented; see [setup and acceptance gates](cloudflare-r2.md).
The comparison below preserves the research behind the original recommendation;
live provider conformance and load tests are still required for R2.

Original recommendation, superseded by the R2 decision: AWS S3 Standard for private result
evidence. Consider Cloudflare R2 if download costs dominate and its live conformance
test passes (or implement a tested R2 adapter). Bunny is attractive for public
marketing/avatar delivery, but its current S3 preview has limits relevant to a
tournament-end upload burst. This is a workload-specific recommendation, not a
claim that any provider is universally fastest.

| Provider | Documented fit | Limitation for Gamics |
| --- | --- | --- |
| AWS S3 Standard | Presigned uploads, SHA256 validation, conditional writes, regional storage | Storage, requests and applicable transfer cost separately; configuration and live load testing are still required |
| Cloudflare R2 Standard | Presigned uploads, conditional writes, no direct Internet egress charge | S3 compatibility is partial; validate our precise checksum HEAD/PUT behavior before claiming drop-in support |
| Bunny Storage | Johannesburg, low storage prices, no API/egress fees | S3 is public preview; documented 500 combined requests/s and 1 Gbps, plus feature gaps |

Bunny's current S3 documentation supports presigned URLs and SHA256 PUT checking;
it would be outdated to say it has no S3 API. However, its HEAD matrix does not
promise the provider-verified SHA256 response our code requires, and conditional
PUT overwrite prevention needs a live check. Its S3 interface lacks versioning,
object locks and automatic lifecycle transitions. The 1 Gbps ceiling means a
10 GB upload burst has a theoretical minimum around 80 seconds, before overhead
and verification downloads. The limits' scope and any increase require confirmation
from Bunny. [Bunny S3](https://bunny.net/docs/storage/s3)

Bunny's older HTTP API uses a zone-wide password in AccessKey. Never ship that
password in the mobile app. A trusted upload gateway could hide it, but then the
gateway has to handle every byte; the S3 route is a better architectural fit if
it meets the required controls. [HTTP API](https://bunny.net/docs/storage/http)

Approximate storage-only monthly examples in USD, assuming decimal GB held for the
full month, excluding tax, compute, databases and delivery/request charges:

| Stored data | Bunny Standard, 1 region | Bunny Standard, 2 regions | R2 Standard |
| --- | --- | --- | --- |
| 10 GB | $1 minimum (raw storage $0.10) | $1 minimum (raw storage $0.20) | $0 if the 10 GB free allowance is unused |
| 300 GB | $3 | $6 | $4.35 after the 10 GB allowance, before any operation charges |

Bunny lists $0.01/GB per region for the first two regions, a $1 monthly minimum,
and separate CDN delivery billing. Edge SSD costs $0.02/GB per region; it is not
automatically necessary for screenshot evidence. [Bunny pricing](https://bunny.net/docs/storage/pricing)

R2 Standard lists $0.015/GB-month, 10 GB-month free, 1 million Class A and 10 million
Class B operations free monthly, then operation fees. Its free tier is shared with
other account usage and billing units round up. [R2 pricing](https://developers.cloudflare.com/r2/pricing/)

AWS pricing varies by region and usage, so obtain a Cape Town estimate rather than
applying a US price to Kenya. AWS documents at least 3,500 write requests/s and
5,500 GET/HEAD requests/s per partitioned prefix, with gradual scaling and retryable
503s during scaling; this is not a guarantee for our whole application.
[S3 pricing](https://aws.amazon.com/s3/pricing/),
[S3 scaling](https://docs.aws.amazon.com/AmazonS3/latest/userguide/optimizing-performance.html),
[S3 presigned URLs](https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-presigned-url.html),
[conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html).

R2's location hints are not a guarantee of a Kenya/South Africa bucket; test upload
latency from Safaricom/Airtel devices, not vendor global CDN averages. Download CDN
performance is not the same as end-user upload performance.
[R2 locations](https://developers.cloudflare.com/r2/reference/data-location/),
[R2 compatibility](https://developers.cloudflare.com/r2/api/s3/api/),
[R2 presigned URLs](https://developers.cloudflare.com/r2/api/s3/presigned-urls/).

Redis complements all three choices by coordinating request/byte allowances. It
does not replace object storage or increase the provider's upload bandwidth. The
implemented verification queue is durable in PostgreSQL, not an evictable cache.
