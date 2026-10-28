// CloudFront Function (cloudfront-js-2.0), viewer request on /billing-upload/*.
// The only accepted path is one multipart part PUT to an upload's source.zip,
// the BFF's sourceKey `accounts/{ACCT_ID}/uploads/{UPLOAD_ID}/source.zip` with
// ACCT_ID and UPLOAD_ID from services/billing/bff/lib/store.js. The S3 presign
// (signed for the regional bucket host) is the authorization; this function
// only narrows which keys can be reached. The pattern has no '%', '.' run or
// empty segment, so encoded slashes, '..' and '//' never match.
var UPLOAD_PART = /^\/billing-upload\/accounts\/[A-Za-z0-9_-]{1,64}\/uploads\/u\d{14}[0-9a-f]{6}\/source\.zip$/;
var PREFIX = '/billing-upload';

function handler(event) {
  var request = event.request;
  if (!UPLOAD_PART.test(request.uri)) {
    return { statusCode: 403, statusDescription: 'Forbidden' };
  }
  request.uri = request.uri.slice(PREFIX.length);
  return request;
}
