import Link from "next/link";
import { site } from "./site";

export type LegalSection = { id: string; title: string; body: React.ReactNode };
export type LegalDoc = { title: string; description: string; updated: string; intro: React.ReactNode; sections: LegalSection[] };

const updated = "10 October 2026";
const email = <a href={`mailto:${site.contactEmail}`}>{site.contactEmail}</a>;

export const terms: LegalDoc = {
  title: "Terms of Service",
  description: "The rules for using Tonits and entering its tournaments.",
  updated,
  intro: (
    <p>
      These terms are the agreement between you and Tonits for using the Tonits app, this website and the tournaments
      we run. By creating an account or entering a tournament, you accept them. If you don&apos;t agree, please
      don&apos;t use Tonits.
    </p>
  ),
  sections: [
    {
      id: "about",
      title: "About Tonits",
      body: (
        <>
          <p>
            Tonits runs online tournaments for eFootball™ Mobile. We are an independent platform. We are not affiliated
            with, endorsed by or sponsored by Konami Digital Entertainment, and eFootball is Konami&apos;s trademark.
          </p>
          <p>
            To play you need your own copy of the game and your own Konami account. Konami&apos;s terms apply to the game
            itself, and you must follow them.
          </p>
        </>
      ),
    },
    {
      id: "eligibility",
      title: "Who can use Tonits",
      body: (
        <ul>
          <li>You must be at least 13 years old.</li>
          <li>
            If you are under 18, or under the age of majority where you live, you need a parent or guardian&apos;s
            permission, and you can only enter free tournaments.
          </li>
          <li>You must be 18 or over to pay an entry fee or receive a prize.</li>
          <li>Taking part must be legal where you live, and you can only enter tournaments open to your country.</li>
          <li>One account per person. Accounts can&apos;t be shared, sold or transferred.</li>
        </ul>
      ),
    },
    {
      id: "account",
      title: "Your account",
      body: (
        <>
          <p>
            You sign up with a username, your Konami ID and a password. The Konami ID you link must be yours, and we may
            ask you to prove it. Keep your details accurate and your password private. You are responsible for what
            happens on your account.
          </p>
          <p>If you think someone else has got into your account, change your password and contact us straight away.</p>
        </>
      ),
    },
    {
      id: "tournaments",
      title: "Tournaments",
      body: (
        <>
          <p>
            Each tournament shows its format, schedule, entry fee, number of places and any prizes before you enter.
            Entering means you accept those details and these terms. Places are first come, first served, and a place
            is only yours once registration is confirmed.
          </p>
          <p>
            We may change a schedule or format, or cancel a tournament, for example when too few players sign up, when
            something breaks, or to keep things fair. If a paid tournament is cancelled you get your entry fee back in
            full, as our <Link href="/refunds">Refund policy</Link> explains.
          </p>
        </>
      ),
    },
    {
      id: "results",
      title: "Playing and reporting results",
      body: (
        <>
          <p>
            You play your matches in eFootball at the scheduled time. Afterwards one player submits the score and the
            other confirms or rejects it. If the opponent doesn&apos;t answer before the deadline, the submitted result
            stands. If it is rejected, each player sends one screenshot of the Full Time screen and we settle it.
          </p>
          <p>
            We may read screenshots automatically, and our staff review disputed results. Our decision is final for the
            tournament. The full process is in our <Link href="/fair-play">Fair play rules</Link>.
          </p>
        </>
      ),
    },
    {
      id: "conduct",
      title: "Fair play and conduct",
      body: (
        <>
          <p>You must not:</p>
          <ul>
            <li>cheat, fix matches, or arrange results with another player;</li>
            <li>submit a false score, or an edited or borrowed screenshot;</li>
            <li>play on someone else&apos;s account, or let someone else play on yours;</li>
            <li>run more than one account;</li>
            <li>harass, threaten or abuse anyone, or post hateful or illegal content;</li>
            <li>exploit bugs, or interfere with Tonits, its systems or other players.</li>
          </ul>
          <p>
            If you do, we may change results, remove you from a tournament, record conduct strikes, withhold prizes, or
            suspend or close your account. Three active strikes stop you entering new tournaments.
          </p>
        </>
      ),
    },
    {
      id: "payments",
      title: "Entry fees and payments",
      body: (
        <p>
          Entry fees are shown in your local currency before you pay. Payments go through providers such as M-Pesa,
          whose own terms also apply. We never see your PIN. Fees are not refundable except as set out in our{" "}
          <Link href="/refunds">Refund policy</Link>. Any charges your provider adds are yours.
        </p>
      ),
    },
    {
      id: "prizes",
      title: "Prizes",
      body: (
        <p>
          When a tournament offers prizes, its page says what they are and how they are paid. Before paying a prize we
          may check your identity, age and eligibility. You are responsible for any tax on what you win. A prize won by
          breaking these terms is forfeited.
        </p>
      ),
    },
    {
      id: "content",
      title: "Your content",
      body: (
        <p>
          You keep the rights to what you upload, such as screenshots and your avatar. You give us permission to use it
          to run Tonits: showing your username and results in brackets, reviewing disputes, and similar. Only upload
          things you have the right to share, and nothing unlawful or offensive.
        </p>
      ),
    },
    {
      id: "public",
      title: "What other people see",
      body: (
        <p>
          Your username, avatar and results appear to other players in the tournaments you enter. You only appear in
          public rankings and player search if you choose to make your profile public, and you can change that at any
          time in the app. Our <Link href="/privacy">Privacy policy</Link> has the details.
        </p>
      ),
    },
    {
      id: "changes",
      title: "Changes and availability",
      body: (
        <p>
          We may change or pause parts of Tonits, for example for maintenance. We don&apos;t control eFootball,
          Konami&apos;s servers or your internet connection, so we can&apos;t promise Tonits will always be available.
          We may update these terms and will tell you about important changes before they apply. Using Tonits after
          that means you accept the new terms.
        </p>
      ),
    },
    {
      id: "ending",
      title: "Closing your account",
      body: (
        <p>
          You can delete your account in the app at any time. Deletion goes ahead after a 24-hour cooling-off period, in
          case you change your mind. We may suspend or close accounts that break these terms. Any refund you are owed is
          still paid.
        </p>
      ),
    },
    {
      id: "liability",
      title: "Liability",
      body: (
        <p>
          Tonits is provided as it is. As far as the law allows, we aren&apos;t liable for indirect losses, or for
          problems caused by the game, its servers or your device and connection. Our total liability to you is limited
          to the fees you paid us in the 12 months before the claim. Nothing in these terms takes away rights you have
          under consumer law that can&apos;t be excluded.
        </p>
      ),
    },
    {
      id: "law",
      title: "Governing law",
      body: (
        <p>
          These terms are governed by the laws of Kenya, and disputes go to the courts of Kenya. Please contact us first
          so we can try to sort things out. If you live elsewhere, you keep any protections your local law gives you.
        </p>
      ),
    },
    {
      id: "contact",
      title: "Contact",
      body: <p>Questions about these terms: {email}.</p>,
    },
  ],
};

export const privacy: LegalDoc = {
  title: "Privacy Policy",
  description: "What Tonits collects, why, who sees it, and your choices.",
  updated,
  intro: (
    <p>
      This policy explains what personal data Tonits collects when you use the app, this website and our tournaments,
      why we use it, who we share it with, how long we keep it, and the rights you have over it.
    </p>
  ),
  sections: [
    {
      id: "controller",
      title: "Who is responsible",
      body: (
        <p>
          Tonits is responsible for your personal data. We follow Kenya&apos;s Data Protection Act 2019 and the data
          protection laws that apply where our players live. You can reach us at {email}.
        </p>
      ),
    },
    {
      id: "collect",
      title: "What we collect",
      body: (
        <ul>
          <li>
            <strong>Account:</strong> your username, Konami ID, password (stored only as a secure hash) and country,
            plus your email address, phone number and date of birth if you add them.
          </li>
          <li>
            <strong>Play:</strong> your in-game name, tournament entries, matches, results, ratings, reports and any
            conduct strikes.
          </li>
          <li>
            <strong>Screenshots:</strong> images you send as match evidence, and what is read from them.
          </li>
          <li>
            <strong>Payments:</strong> amount, currency, the M-Pesa phone number and receipt, and payment status. We
            never receive your PIN.
          </li>
          <li>
            <strong>Device and security:</strong> IP address, device and app version, push notification token,
            sign-in sessions and security logs.
          </li>
          <li>
            <strong>Messages:</strong> what you send our support team.
          </li>
        </ul>
      ),
    },
    {
      id: "use",
      title: "How we use it",
      body: (
        <>
          <ul>
            <li>to run your account and the tournaments you enter;</li>
            <li>to verify results, including reading screenshots and reviewing disputes;</li>
            <li>to take payments and pay refunds and prizes;</li>
            <li>to keep Tonits fair and secure, and to enforce our rules;</li>
            <li>to send you match reminders, result confirmations and other notifications;</li>
            <li>to understand how Tonits is used, through combined statistics;</li>
            <li>to meet our legal obligations.</li>
          </ul>
          <p>
            We rely on our contract with you, our legitimate interest in running a fair and secure platform, your
            consent where we ask for it (you can withdraw it at any time), and our legal obligations.
          </p>
        </>
      ),
    },
    {
      id: "screenshots",
      title: "Reading screenshots",
      body: (
        <p>
          When a result is disputed, we read the scores from both players&apos; screenshots automatically. If our own
          reader is unsure, a screenshot may be passed to an AI service run by a provider that works under contract with
          us and may only use it to give us the reading. A person reviews disputed results before any conduct strike is
          recorded.
        </p>
      ),
    },
    {
      id: "public",
      title: "What is public",
      body: (
        <p>
          Other players in your tournaments see your username, avatar and results. You only appear in public rankings
          and player search if you make your profile public, which is off until you turn it on. Your Konami ID, email,
          phone number and payment details are never shown publicly.
        </p>
      ),
    },
    {
      id: "sharing",
      title: "Who we share it with",
      body: (
        <>
          <ul>
            <li>payment providers such as Safaricom (M-Pesa), to take payments and pay refunds;</li>
            <li>hosting, storage and infrastructure providers that run Tonits for us;</li>
            <li>Apple and Google, to deliver push notifications;</li>
            <li>providers that help us read screenshots;</li>
            <li>professional advisers, and authorities when the law requires it;</li>
            <li>a buyer or successor if Tonits is sold or reorganised.</li>
          </ul>
          <p>
            We don&apos;t sell your personal data. Partners only ever receive combined, de-identified statistics, never
            your phone number, email, payment details, messages or screenshots.
          </p>
        </>
      ),
    },
    {
      id: "transfers",
      title: "International transfers",
      body: (
        <p>
          Some of our providers process data outside your country. When they do, we use contracts and safeguards that
          protect your data to the standard the law requires.
        </p>
      ),
    },
    {
      id: "retention",
      title: "How long we keep it",
      body: (
        <p>
          We keep your account data while your account is open. When you delete your account we delete or anonymise
          your personal data, except what we must keep: payment records for as long as tax and financial law requires,
          and match results, screenshots and review records for as long as needed to settle disputes and protect the
          integrity of past tournaments. Anonymised results may stay in tournament history.
        </p>
      ),
    },
    {
      id: "children",
      title: "Children",
      body: (
        <p>
          Tonits isn&apos;t for children under 13. Players aged 13 to 17 need a parent or guardian&apos;s permission and
          can only enter free tournaments. Their profiles are private, and their data is never used for commercial
          profiling.
        </p>
      ),
    },
    {
      id: "rights",
      title: "Your rights",
      body: (
        <>
          <p>
            You can ask to see, correct, export or delete your data, object to or restrict how we use it, and withdraw
            consent. You can edit your profile, choose whether it is public and delete your account in the app; for
            anything else, email {email}. We answer within the time the law sets.
          </p>
          <p>
            You can also complain to a data protection authority. In Kenya that is the Office of the Data Protection
            Commissioner.
          </p>
        </>
      ),
    },
    {
      id: "security",
      title: "Security",
      body: (
        <p>
          We encrypt data in transit, store passwords only as secure hashes, limit staff access to what each role needs
          and keep audit logs of sensitive actions. No system is perfectly secure, so please use a strong password of
          your own.
        </p>
      ),
    },
    {
      id: "cookies",
      title: "Cookies",
      body: (
        <p>
          This website doesn&apos;t use advertising or tracking cookies. It may store small amounts of information in
          your browser that it needs to work.
        </p>
      ),
    },
    {
      id: "changes",
      title: "Changes",
      body: (
        <p>
          We will update this policy when what we do changes, and tell you about important changes in the app before
          they apply.
        </p>
      ),
    },
  ],
};

export const refunds: LegalDoc = {
  title: "Refund Policy",
  description: "When entry fees are refunded and how refunds are paid.",
  updated,
  intro: (
    <p>
      If you pay to enter a tournament and don&apos;t get a place you are owed, you get your money back in full. This
      page explains when that happens and how.
    </p>
  ),
  sections: [
    {
      id: "fees",
      title: "Entry fees",
      body: (
        <p>
          The entry fee is shown in your local currency before you pay. In Kenya you pay with M-Pesa; other payment
          methods will be added as we open in more countries. Your place is confirmed only once your payment has been
          verified.
        </p>
      ),
    },
    {
      id: "full-refund",
      title: "When you get a full refund",
      body: (
        <ul>
          <li>the tournament is cancelled;</li>
          <li>
            your payment arrived after the places filled, registration closed or the draw was made, so no place was
            available;
          </li>
          <li>you were not eligible to play when your payment completed, for example because of a conduct suspension;</li>
          <li>you were charged more than once for the same entry;</li>
          <li>you withdraw before the draw is made.</li>
        </ul>
      ),
    },
    {
      id: "no-refund",
      title: "When fees are not refunded",
      body: (
        <ul>
          <li>you withdraw after the draw is made, or don&apos;t turn up for your matches;</li>
          <li>you are removed from a tournament for breaking our rules;</li>
          <li>you lose, or are knocked out;</li>
          <li>you have problems on your side, such as your connection, device or game updates.</li>
        </ul>
      ),
    },
    {
      id: "problems",
      title: "If a payment goes wrong",
      body: (
        <p>
          If you have been charged but the app shows your payment as under review, don&apos;t pay again. We check with
          the payment provider and either confirm your place or refund you. If it doesn&apos;t resolve, contact us with
          your M-Pesa receipt number.
        </p>
      ),
    },
    {
      id: "how",
      title: "How refunds are paid",
      body: (
        <p>
          Refunds go back to the M-Pesa number or payment method you paid with. Once a refund is due, the app shows it as
          pending, and as refunded with a receipt once it has been paid. We aim to pay refunds within 7 business days.
        </p>
      ),
    },
    {
      id: "contact",
      title: "Contact",
      body: <p>Questions about a payment or refund: {email}. Please include your M-Pesa receipt number.</p>,
    },
  ],
};

export const fairPlay: LegalDoc = {
  title: "Fair Play Rules",
  description: "How results are reported, how disputes are settled and what earns a strike.",
  updated,
  intro: (
    <p>
      Every Tonits result is checked by both players. These rules explain how that works, how disputes are settled and
      what happens when someone breaks the rules.
    </p>
  ),
  sections: [
    {
      id: "before",
      title: "Before your match",
      body: (
        <p>
          Be ready at the scheduled time and play on the Konami account linked to your Tonits profile. A tournament page
          may add its own rules, such as match settings. Those apply too.
        </p>
      ),
    },
    {
      id: "reporting",
      title: "Reporting the result",
      body: (
        <ol>
          <li>After the match, either player submits the final score.</li>
          <li>The other player gets a notification to confirm or reject it.</li>
          <li>If they confirm, the result is final.</li>
          <li>If they don&apos;t answer before the deadline, the submitted result stands.</li>
        </ol>
      ),
    },
    {
      id: "disputes",
      title: "Disputes",
      body: (
        <>
          <p>
            If the result is rejected, each player sends one screenshot of the Full Time screen before the deadline. We
            read and compare the two, and our staff decide: accept the submitted result, correct the score, or remove
            both players.
          </p>
          <p>A player who doesn&apos;t send a screenshot in time can be removed from the tournament.</p>
        </>
      ),
    },
    {
      id: "screenshots",
      title: "Screenshots",
      body: (
        <p>
          Send the whole Full Time screen from that match, exactly as the game shows it. Cropping, editing or sending a
          screenshot from another match is a serious offence and can lead to suspension.
        </p>
      ),
    },
    {
      id: "strikes",
      title: "Conduct strikes",
      body: (
        <>
          <p>
            When a review shows a player was in the wrong, for example by rejecting a correct result or submitting a
            false score, staff can record a conduct strike against them. You are told when a strike is recorded or
            removed.
          </p>
          <p>
            Three active strikes stop you entering new tournaments. Entries you already hold are not affected. If you
            think a strike is wrong, contact us at {email}.
          </p>
        </>
      ),
    },
    {
      id: "banned",
      title: "Never allowed",
      body: (
        <ul>
          <li>cheating, match fixing or arranging results;</li>
          <li>false scores or edited screenshots;</li>
          <li>playing on someone else&apos;s account, or letting someone play on yours;</li>
          <li>running more than one account;</li>
          <li>harassing or abusing other players.</li>
        </ul>
      ),
    },
  ],
};
