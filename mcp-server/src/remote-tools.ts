import type { Tool } from '@modelcontextprotocol/sdk/types.js';

export const MAX_HTTP_REQUEST_BYTES = 1024 * 1024;
export const MAX_HTTP_UPLOAD_BYTES = 512 * 1024;
export const MAX_HTTP_RESULT_BYTES = 32_000;
export const MAX_HTTP_API_BYTES = 1024 * 1024;

export function mutationReceipt(name: string, value: string): string | undefined {
  if (!/^ttobak_(create_|update_|put_|upload_|add_|link_|unlink_|kb_(upload|sync|delete_))/.test(name)) return;
  const ids: Record<string, string> = {};
  try {
    const data = JSON.parse(value);
    for (const key of ['projectId', 'docId', 'accountId', 'fileId', 'userId', 'jobId']) {
      if (typeof data?.[key] === 'string' && /^[\w-]{1,128}$/.test(data[key])) ids[key] = data[key];
    }
  } catch { /* A successful non-JSON response still confirms completion. */ }
  return JSON.stringify({ status: 'completed', ...ids, responseOmitted: true,
    message: 'Write succeeded. Large response omitted; do not repeat the write. Read the saved record to inspect it.' });
}

export function httpTools(tools: Tool[]): Tool[] {
  return tools.filter((tool) => !['ttobak_login', 'ttobak_logout'].includes(tool.name))
    .map((tool) => {
      if (!['ttobak_kb_upload', 'ttobak_upload_document'].includes(tool.name)) return tool;
      const properties = { ...tool.inputSchema.properties };
      delete properties.filePath;
      properties.fileName = { type: 'string', minLength: 1, maxLength: 255, description: 'File name, not a local path' };
      properties.contentBase64 = {
        type: 'string', minLength: 4, maxLength: 4 * Math.ceil(MAX_HTTP_UPLOAD_BYTES / 3),
        description: 'Standard base64 file bytes, at most 512 KiB decoded. No server filesystem access.',
      };
      return {
        ...tool,
        description: 'Upload caller-supplied file bytes (HTTP, maximum 512 KiB). ' +
          (tool.name === 'ttobak_kb_upload' ? 'Adds a file to your knowledge base.' : 'Creates a personal or account document.'),
        inputSchema: {
          ...tool.inputSchema, properties, additionalProperties: false,
          required: [...(tool.inputSchema.required ?? []).filter((name) => name !== 'filePath' && name !== 'fileName'),
            'fileName', 'contentBase64'],
        },
      };
    });
}

export function decodeUpload(args: Record<string, unknown>): { data: Buffer; fileName: string } {
  if ('filePath' in args) throw new Error('filePath is not accepted over HTTP; supply contentBase64');
  const { fileName, contentBase64 } = args;
  if (typeof fileName !== 'string' || !fileName.trim() || fileName.length > 255 ||
      /[/\\\x00-\x1f\x7f]/.test(fileName) || fileName === '.' || fileName === '..') {
    throw new Error('fileName must be a plain file name');
  }
  if (typeof contentBase64 !== 'string' || contentBase64.length > 4 * Math.ceil(MAX_HTTP_UPLOAD_BYTES / 3) ||
      contentBase64.length % 4 !== 0 || !/^[A-Za-z0-9+/]*={0,2}$/.test(contentBase64)) {
    throw new Error('contentBase64 must be standard base64, at most 512 KiB decoded');
  }
  const data = Buffer.from(contentBase64, 'base64');
  if (!data.length || data.length > MAX_HTTP_UPLOAD_BYTES || data.toString('base64') !== contentBase64) {
    throw new Error('contentBase64 must encode 1–524288 bytes');
  }
  return { data, fileName };
}
