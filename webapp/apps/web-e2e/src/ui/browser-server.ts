import { createServer, request as proxyRequest } from 'node:http';
import { webUrl } from '../e2e-env';
import type { isolatedDaemon } from '../enclave-helpers';

// Serve Vite and the isolated real daemon at one origin, including streaming SSE.
// No provider responses or lifecycle endpoints are mocked in these UI tests.
export async function browserServer(
  daemon: Awaited<ReturnType<typeof isolatedDaemon>>,
) {
  const server = createServer((request, response) => {
    const target = new URL(
      request.url ?? '/',
      request.url?.startsWith('/api/')
        ? daemon.url
        : webUrl,
    );
    const upstream = proxyRequest(
      target,
      {
        method: request.method,
        headers: {
          ...request.headers,
          host: target.host,
          origin: target.origin,
          authorization: `Bearer ${daemon.token}`,
        },
      },
      (incoming) => {
        response.writeHead(incoming.statusCode ?? 502, incoming.headers);
        incoming.pipe(response);
      },
    );
    upstream.on('error', () => {
      response.writeHead(502);
      response.end();
    });
    response.on('close', () => upstream.destroy());
    request.pipe(upstream);
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  if (!address || typeof address === 'string')
    throw new Error('proxy has no address');
  return {
    url: `http://127.0.0.1:${address.port}`,
    close: async () => {
      server.closeAllConnections();
      await new Promise<void>((resolve, reject) =>
        server.close((error) => (error ? reject(error) : resolve())),
      );
    },
  };
}
