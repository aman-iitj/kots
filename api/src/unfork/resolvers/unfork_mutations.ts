import { Context } from "../../context";
import { tracer } from "../../server/tracing";
import { CreateUnforkSessionMutationArgs, UpdateSession } from "../../generated/types";

interface Error {
  result: string
}

export function UnforkMutations(stores: any) {
  return {
    async createUnforkSession(root: any, args: CreateUnforkSessionMutationArgs, context: Context): Promise<Error|UpdateSession> {
      const span = tracer().startSpan("mutation.createUnforkSession");

      const { upstreamUri, forkUri } = args;

      const unforkSession = await stores.unforkStore.createUnforkSession(span.context(), context.session.userId, upstreamUri, forkUri);
      const deployedUnforkSession = await stores.unforkStore.deployUnforkSession(span.context(), unforkSession.id!);

      // Until we have unfork headed mode, we just create an update haded job to allow for UI
      const now = new Date();
      const abortAfter = new Date(now.getTime() + (1000 * 60));
      while (new Date() < abortAfter) {
        const maybeUpdatedSession = await stores.unforkStore.getSession(span.context(), deployedUnforkSession.id!);
        if (maybeUpdatedSession.result === "failed") {
          span.finish();

          return {
            result: "error unforking application",
          };

        } else if (maybeUpdatedSession.result === "completed") {
          const updateSession = await stores.updateStore.createUpdateSession(span.context(), context.session.userId, maybeUpdatedSession.id!);
          const deployedUpdateSession = await stores.updateStore.deployUpdateSession(span.context(), updateSession.id!);

          span.finish();

          return deployedUpdateSession;
        }

        await sleep(100);
      };

      span.finish();

      return {
        result: "timeout unforking application",
      };
    }
  }
}

function sleep(ms = 0) {
  return new Promise(r => setTimeout(r, ms));
}
