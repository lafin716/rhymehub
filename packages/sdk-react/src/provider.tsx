import { createContext, useContext, useMemo, type ReactNode } from "react";
import { RhymehubClient, type RhymehubClientOptions } from "@rhymehub/sdk";

export const RhymehubContext = createContext<RhymehubClient | null>(null);

export interface RhymehubProviderProps {
  /** Use a client you created yourself. Takes precedence over `options`. */
  client?: RhymehubClient;
  /**
   * Create a client from options. Keep the referenced values stable (module constants or
   * env values): the client is rebuilt only when projectId, endpoint or projectKey change.
   */
  options?: RhymehubClientOptions;
  children?: ReactNode;
}

export function RhymehubProvider({ client, options, children }: RhymehubProviderProps) {
  const projectId = options?.projectId;
  const endpoint = options?.endpoint;
  const projectKey = options?.projectKey;
  const value = useMemo(() => {
    if (client) return client;
    if (!options) throw new Error("RhymehubProvider needs either `client` or `options`");
    return new RhymehubClient(options);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- options is intentionally keyed by its primitive fields
  }, [client, projectId, endpoint, projectKey]);
  return <RhymehubContext.Provider value={value}>{children}</RhymehubContext.Provider>;
}

/** Resolve the client: explicit argument first, then the nearest provider. Throws when neither exists. */
export function useClient(explicit?: RhymehubClient): RhymehubClient {
  const fromContext = useContext(RhymehubContext);
  const client = explicit ?? fromContext;
  if (!client) {
    throw new Error("No rhymehub client: wrap the app in <RhymehubProvider> or pass a `client` prop.");
  }
  return client;
}

/** The client from the nearest <RhymehubProvider>. */
export function useRhymehubClient(): RhymehubClient {
  return useClient();
}
