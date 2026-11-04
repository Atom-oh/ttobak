// Daily: pull the AWS What's New RSS feed, keep announcements that match a billing-relevant topic
// (classified from title/description only), write whats_new_feed.json to S3. The feed is public and shared by every account.
import { S3Client, GetObjectCommand, PutObjectCommand } from '@aws-sdk/client-s3';

const FEED = 'https://aws.amazon.com/about-aws/whats-new/recent/feed/';
const { BUCKET, KEY = 'data/whats_new_feed.json', KEEP_DAYS = '180' } = process.env;
const s3 = new S3Client({});

// topic -> pattern: services and cost levers that show up in AWS billing
const TOPICS = {
  'Direct Connect': /direct connect/i,
  'Savings Plans·RI': /savings plans?|reserved instance/i,
  'Graviton': /graviton|\b[cmr][78]g[a-z]*\b/i,
  '8세대 인스턴스': /\b[cmr]8[gia]\b|\b[cmr]8i-flex\b/i,
  'GPU·추론 칩': /\b(g6e?|g7e?|p5e?|p6|inf2|trn2|trainium|inferentia)\b/i,
  'EKS·Karpenter': /\beks\b|karpenter/i,
  'SageMaker': /sagemaker/i,
  'Bedrock': /bedrock/i,
  'Claude': /claude|anthropic/i,
  'OpenAI on Bedrock': /openai|gpt-/i,
  'AgentCore': /agentcore/i,
  'Lambda Web Functions·Lambda': /lambda web functions?|\baws lambda\b/i,
  'CloudFront': /cloudfront/i,
  'WAF·Shield': /\bwaf\b|shield/i,
  'Inspector': /inspector/i,
  'OpenSearch': /opensearch/i,
  'RDS·Aurora': /\brds\b|aurora/i,
  'S3 스토리지': /\bamazon s3\b|s3 tables|intelligent-tiering/i,
  'DynamoDB': /dynamodb/i,
  'EMR': /\bemr\b/i,
  'Kinesis·Firehose': /kinesis|firehose/i,
  '도쿄 리전': /asia pacific \(tokyo\)|ap-northeast-1/i,
  '상파울루 리전': /south america \(s[aã]o paulo\)|sa-east-1/i,
  'Marketplace': /aws marketplace/i,
  'AZ 간 전송': /cross-az|availability zone affinity|inter-az/i,
};

const decode = (s) => s.replace(/<!\[CDATA\[([\s\S]*?)\]\]>/g, '$1').replace(/<[^>]+>/g, ' ')
  .replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/\s+/g, ' ').trim();
const tag = (item, name) => { const m = item.match(new RegExp(`<${name}[^>]*>([\\s\\S]*?)</${name}>`)); return m ? decode(m[1]) : ''; };

const IRRELEVANT = /govcloud|china\)|china regions|\(beijing\)|\(ningxia\)/i; // regions the platform's accounts do not run in

export function classify(title, description) {
  if (IRRELEVANT.test(title)) return { topics: [] };
  const text = `${title} ${description.slice(0, 240)}`;
  const topics = Object.entries(TOPICS).filter(([, re]) => re.test(text)).map(([t]) => t);
  return { topics };
}

export function parseFeed(xml) {
  return [...xml.matchAll(/<item>([\s\S]*?)<\/item>/g)].map(([, it]) => {
    const title = tag(it, 'title');
    const url = tag(it, 'link');
    const pub = new Date(tag(it, 'pubDate'));
    return { title, url, date: isNaN(pub) ? '' : pub.toISOString().slice(0, 10), description: tag(it, 'description').slice(0, 600) };
  }).filter((x) => x.url.startsWith('https://aws.amazon.com/'));
}

async function readExisting() {
  try {
    const o = await s3.send(new GetObjectCommand({ Bucket: BUCKET, Key: KEY }));
    return JSON.parse(await o.Body.transformToString());
  } catch (e) {
    if (e.name === 'NoSuchKey') return [];
    throw e;
  }
}

export async function handler() {
  const fetchedAt = new Date().toISOString();
  const res = await fetch(FEED, { headers: { 'User-Agent': 'billing-insight-whatsnew/1.0' } });
  if (!res.ok) throw new Error(`feed HTTP ${res.status}`);
  const items = parseFeed(await res.text());
  const matched = items.map((it) => ({ ...it, ...classify(it.title, it.description), source: 'rss', fetched_at: fetchedAt }))
    .filter((it) => it.topics.length);

  const cutoff = new Date(Date.now() - Number(KEEP_DAYS) * 864e5).toISOString().slice(0, 10);
  const byUrl = new Map();
  for (const it of await readExisting()) if (it.source === 'rss' && it.date >= cutoff) byUrl.set(it.url, it);
  for (const it of matched) byUrl.set(it.url, it);
  const rows = [...byUrl.values()].map(({ description, ...r }) => r).sort((a, b) => b.date.localeCompare(a.date));

  await s3.send(new PutObjectCommand({ Bucket: BUCKET, Key: KEY, Body: JSON.stringify(rows), ContentType: 'application/json' }));
  const summary = { fetchedAt, feedItems: items.length, matched: matched.length, stored: rows.length };
  console.log(JSON.stringify(summary));
  return summary;
}
