# Writing training texts for Promptdam's firewall model

You write realistic texts in one language, with every piece of personal data, special-category data and secret
marked. A program turns your marks into exact labels, so **what you mark is what the model learns**: mark every
instance, mark nothing else, and keep each mark tight around the value.

These texts teach a model that runs inside an AI gateway. It reads what applications and coding agents send to
AI providers (customer-support conversations, documents, tool results, source code) and finds what must not
leave the company. Write the kinds of text such traffic really contains.

## File format

Your assignment file lists the texts to write, one per line: an id, then what the text should be. Write them in
order into the output file you are given, each starting with a header line holding only `@@@` and its id:

```
@@@ tr-0001
Müşteri: Merhaba, ben ⦃person|Ayşe Yılmaz⦄. Siparişim hâlâ gelmedi.
Temsilci: Merhaba Ayşe Hanım, sipariş numaranızı alabilir miyim?
@@@ tr-0002
...
```

Everything up to the next `@@@` line belongs to the text. No blank line is needed between texts; blank lines
inside a text are kept.

## Marks

- `⦃label|value⦄` marks a value you write yourself. The value is exactly what appears in the text; nothing else
  goes inside the braces. Use the characters `⦃` (U+2983), `|` and `⦄` (U+2984) exactly.
- `⦃=generator⦄` asks the program to fill in a generated value **and label it** (the generator decides the
  label). Use it for every secret and every ID number with a checksum (list below). Never write a real-looking
  key or ID number yourself.
- `⦃~generator⦄` asks for a generated value that is **not** personal data and must stay unlabeled: hashes,
  UUIDs, publishable keys, documentation example keys. These are the look-alikes the model must learn to leave
  alone.
- Marks never nest and never span two values. Mark each occurrence: if a name appears three times, mark it three
  times, including a shortened form (`⦃person|Ayşe⦄` later). This covers every label: a medication named again,
  "the diabetes" referring back, a conviction mentioned again in a later turn, a union named again. Subject
  lines, headers, tool-call arguments and side remarks are text too.
- A generated value repeated in one text (the same person's ID in a form and in a note, one key in `.env` and in
  a config) takes a ref after `#`: every `⦃=za_id#1⦄` in a text gets the same value, `⦃=za_id#2⦄` another. Without
  a ref each mark gets a new value.
- **Where a mark ends.** Inside: the value and what is fused to it in writing (a case ending or clitic written
  as one word: Hungarian `Budapesten`, Persian `ویلچریم`). Outside: a suffix after an apostrophe or a hyphen
  (`⦃person|Ayşe⦄'nin`), articles, prepositions, copulas and auxiliaries (`the`, `de`, `som`,
  `je`, `is`), quotes and brackets around a value, titles and honorifics, and words
  that only describe the value (`the physio says`, `manual review`). Quotes stay inside only when they belong to
  a legal name (`ТОВ «Альфа»`).
- For special categories mark the shortest phrase that reveals the fact by itself: `⦃trade_union|member of
  Unite⦄`, `⦃trade_union|shop steward⦄`, `⦃health|on metformin⦄`; not the whole sentence around it.
- A value the text says is **not** real or not about a person (a hypothetical, a negation, a question or
  someone's guess, a rule about a class of people, a template field name like "Name:" with no value, a
  placeholder like `<customer name>`) is not marked. Test data counts as real when it looks real: mark a
  realistic name or email in a fixture or seed file (masking it costs nothing, letting a real one through does);
  leave only obvious dummies (`John Doe`, `Test Testic`, `name@example.com`, `10.0.0.5` in a doc example).

## Labels

Personal data:
- `person`: a private individual's name in any form (full, given name alone, surname alone, initials with a
  surname, a nickname used for them). Titles and honorifics stay outside the mark (`Ayşe Hanım` →
  `⦃person|Ayşe⦄ Hanım`; `Dr. ⦃person|Weber⦄`).
- `username`: an account handle or login (`@ayse_y`, `ayilmaz`).
- `email`, `phone`: as written, including country code and separators.
- `address`: a street or postal address line, building, flat, postal code, a partial address too (a street
  with no number, a flat or complex named as someone's home: `unit 4B at Bellville Gardens`). Write the whole
  address as one mark when it is written together on one line; mark each line separately when it spans lines. A
  company's own address, branch or seat is not marked.
- `location`: a city, district or region when it says where a person lives, was born, works or is, in a form
  field or in a sentence. A country alone is not marked, nor a booking's destination, nor where a company or
  branch is.
- Data that belongs to an organization is not personal: its switchboard and support numbers, role mailboxes
  (`support@`, `hr@`, `info@`), its website. A person's work email or direct line is personal.
- `date_of_birth`: a birth date in any format. `date`: another date about a person's life, a bare month or year
  included: hire, start, dismissal and exit dates, leave and sick leave, admission and discharge, appointments,
  court dates and hearings, arrests, moves, weddings, deaths, an incident that happened to them. Business dates
  (delivery, invoice due, release, deploy, meeting time, a payment's booking date) are not marked.
- `government_id`: national ID, social security, passport, driver's licence, tax ID, health-insurance number.
  Use generators for those with checksums.
- `account`: an identifier that points to one person: bank account, IBAN, card number, customer, member,
  policy, loyalty, employee, student, patient, beneficiary, contract, meter or installation numbers, a vehicle
  plate, a platform user id (in prose, JSON, CSV or SQL rows alike). Identifiers of a thing or an event are
  **not** marked: order, invoice, ticket, case, shipment and tracking numbers, database row ids of records that
  aren't people. Support assistants need those.
- `ip`: an IP address. `url_personal`: a link that identifies a person (a profile, a personal site, a link with
  their name or ID in it).

Special categories (GDPR Articles 9 and 10), marked as the phrase that reveals it:
- `health`: conditions, diagnoses, symptoms, medications, treatments, procedures, tests and their results,
  aids (a wheelchair, a CPAP machine), allergies, disabilities and disability grades or cards, pregnancy, the
  reason for sick leave, mental health, and a visit or benefit that reveals a condition (`⦃health|tip 2
  diyabet⦄`, `⦃health|allergic to penicillin⦄`, `her ⦃health|dialysis⦄ on Tuesdays`).
- `religion`: religious or philosophical belief or practice (`⦃religion|practising Catholic⦄`).
- `sexual_orientation`: sexual orientation, sex life, gender identity.
- `political_opinion`: party membership, political views or activity.
- `trade_union`: union membership or activity, a union office (shop steward), union dues deducted.
- `criminal_record`: convictions, offences, arrests, charges, probation.
- `ethnicity`: racial or ethnic origin.
- `biometric_genetic`: fingerprints, face templates, DNA, genetic test results.

Secrets (always through a generator):
- `secret`: an API key, token, private key, password, or the password in a connection string. Mark only the
  value, never the variable or field name. In code, `API_KEY = "⦃=stripe_secret⦄"` is right; `⦃=stripe_secret⦄`
  inside a string the code reads from the environment is wrong (`os.Getenv("STRIPE_KEY")` holds no secret). A
  password in a connection string or config is `⦃=password⦄`. Well-known local defaults (`postgres:postgres`,
  `root`/`root`, `changeme` in a docker-compose for development) are not secrets: write them unmarked, the model
  must learn not to block them. A one-time code (an SMS OTP) is not marked.

Context, marked so the model learns the boundary (the firewall never masks these):
- `organization`: a company, institution, hospital, school or authority.
- `job_title`: a role or position.
- `money`: an amount, salary or price.
- `public_figure`: a well-known public person (a head of state, a famous athlete) mentioned as such, titles
  outside as for `person`.

## Generators

Labeled (`⦃=…⦄`). IDs with checksums: `tckn`, `aadhaar`, `pan_in`, `cpf`, `cnpj`, `cn_id`, `my_number`, `kr_rrn`,
`sg_nric`, `au_tfn`, `au_medicare`, `uk_nino`, `nhs`, `de_steuer_id`, `fr_nir`, `es_dni`, `es_nie`, `it_cf`,
`nl_bsn`, `pl_pesel`, `se_pnr`, `fi_hetu`, `dk_cpr`, `no_fnr`, `be_nn`, `pt_nif`, `mx_curp`, `mx_rfc`, `ca_sin`,
`za_id`, `il_id`, `ae_eid`, `us_ssn`, `us_itin`, `passport` (letters and digits), `iban_<cc>` (e.g. `iban_de`,
`iban_tr`), `card`. Secrets: `stripe_secret`, `stripe_restricted`, `aws_access_key`, `aws_secret`, `gcp_api_key`,
`github_token`, `github_pat`, `gitlab_token`, `slack_token`, `slack_webhook`, `openai_key`, `anthropic_key`,
`huggingface_token`, `npm_token`, `twilio_auth`, `sendgrid_key`, `mailgun_key`, `shopify_token`, `discord_token`,
`telegram_token`, `datadog_key`, `azure_key`, `jwt`, `private_key` (a whole PEM block), `password`,
`generic_token` (a key from a vendor nobody knows: the model must still catch it), `basic_auth` (the base64 part
of `Authorization: Basic …`), `bearer` (the token after `Bearer `).

Unlabeled look-alikes (`⦃~…⦄`): `uuid`, `sha1`, `sha256`, `git_sha`, `stripe_publishable`, `example_aws_key`,
`placeholder_key` (`your-api-key-here` and friends), `public_key` (an SSH public key), `base64_blob`,
`order_number`, `tracking_number`, `invoice_number`, `timestamp`. Each fits where its name says (a uuid where an
id goes, a sha where a commit goes); write comments and field names that match what the program fills in.

## How to write

- **Native, not translated.** Names, addresses, phone formats, institutions, customs and phrasing of the
  language's own country (or countries: write PT-BR as Brazil, FR-CA as Québec). Vary regions, ages, genders,
  social backgrounds, naming customs (patronymics, two surnames, initials, honorifics).
- **Realistic traffic.** The assignment says the domain and format. Chats have several turns between people or
  between a user and an assistant; tool results look like what a program returns (JSON, CSV, SQL rows, logs);
  code looks like real code in the stated language with comments and strings where people put them.
- **Density varies.** Some texts are dense with personal data, some have one item, and the assignment asks for
  some with none at all (still realistic, with organizations, amounts, dates and order numbers that are not
  personal data).
- **Length varies** as the assignment says: short (1 to 3 sentences), medium (a paragraph or a short exchange),
  long (300 to 600 words).
- **Hard cases on purpose:** a name that is also a common word or a place; a surname alone; a health detail said
  casually ("I've been off since my chemo started"); a secret inside a connection string, a curl command, a
  `.env` file, a stack trace, a JSON config; a negated or hypothetical mention ("if a customer's IBAN were
  wrong…") left unmarked.
- **Nothing real.** Invent every private person, address and number. Public figures appear only as
  `public_figure` and only in a public role.
- Write the language's own script, punctuation and number formats. Code identifiers and keywords stay as the
  programming language has them; comments and strings follow the assignment's language.
