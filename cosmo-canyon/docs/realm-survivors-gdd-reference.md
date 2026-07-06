REALM SURVIVORS
Summary
        Realm Survivors is a casual-focused mobile game where players survive hordes of enemies by automatically firing screen-clearing attacks while gathering upgrades to continuously scale their offensive build.
Pillars
* Casual Focused, with hardcore gamer gameplay unlocked over time
* Casual, lighthearted fantasy feel, without being too irreverent. Not targeted for children, but children can still play and have fun.
* Is a Survivors-like rogue-lite
   * Player starts out weak, but quickly feels overpowered
   * Over-the-top weapons auto-firing all the time
   * Player’s main focuses are movement (avoiding enemies and picking up loot) and choosing item upgrades
   * The stage is always moving down (so it’s like the player is moving up) and enemies move down with the map
* Feeling of constant progression, always with a clear goal and next step
* Character interactions that let you feel a bond with each character
* Dungeons and Dragons-style fantasy-based, but not actually using copyrighted material (no Beholders, etc.)
* Bite-size gameplay loop, lasting around five minutes (plus boss fight) at a time.
* High amounts of content to discover.


Concepts
* The Realm: The world in which our game takes place (narratively).
* Adventure: A series of 5 Stages. The player keeps all of their Weapons and temporary per-Adventure upgrades for the entire Adventure. An Adventure has a narrative, and before each stage, players can choose between two options. Each option is either:
   * A different stage. E.g. “You come across a fork in the road. Go left or right? (left is Forest, right is Wheat Fields)
   * A different way to resolve a situation. E.g. “You stop to rest for the night before continuing. Go to sleep, or maintain equipment first?” Sleep gives +25 Max HP next match; Maintain gives +10% Damage next match.
* Stage or Run or Match: A single instance in which the player goes through waves coming in from the top, with a boss at the end. Stages should each have at least one unique event during it, like walls of enemies, a sandstorm, etc. There is a timer at the top. The Boss should appear exactly five minutes into a Stage. The timer should keep counting up while fighting the boss.
* Health: The player starts with 100 Health by default. Reaching 0 means the player dies. The player heals to full between Stages.
* Weapon: Primary active skills/items that generally auto-fire at a regular interval. Some might instead fire on a trigger, like when taking damage. Some are constant effects, like dealing damage to nearby enemies.
   * The player can only have one copy of each Tier 1 weapon at a time, though if they upgrade the Tier of that weapon, it’s technically a different weapon so they will possibly be able to get another copy of the original Tier 1 weapon. (this allows them to carry all of the legendary Tier 3 swords that evolve from the Tier 1 Sword, for example, but never two copies of the Tier 1 Sword at once)
   * While no two weapons should be exactly the same, it’s fine to have some overlap. For example, at level 1 the Javelin would throw one projectile at a target, while the Throwing Knives would throw three projectiles in a fan at a target.
* Weapon Slots: By default, the player can carry six weapons.
* Weapon Level: Weapons start at Level 1 can be leveled up to 5. Each level increases the weapon’s effectiveness, either by directly increasing damage, increasing another stat (like projectile speed or area of effect), or adding to the weapon’s mechanic (like adding another projectile). Generally, levels 3 and 5 should add to the weapon’s damage/stats. Levels 2 and 4 might add something mechanically (on some weapons).
* Weapon Tier: Weapons start at Tier 1. When they hit max level, their level cannot be upgraded further. However, they can be Evolved to the next Tier through special means (like finding a Chest). Generally, Tier 2 is the “enchanted” version of the original weapon, with some kind of bonus property that adds to the weapon’s mechanics. Tier 3 is a “legendary” version, often a large change from the original.
* Evolve: By default, weapons can be Evolved up to Tier 3. Evolving a weapon brings it down to level 1. Some weapons can branch out into multiple possibilities at certain tiers.
   * Example: A Sword (Tier 1) can evolve into an Enchanted Sword (Tier 2). Enchanted Sword can evolve into either a Flametongue, Frostbrand, or Thunderfury(each Tier 3).
* Pickups: Exp Orbs, Powerups, and Chests are pickups. The stage is constantly scrolling down, but these don’t scroll with the stage. This is so that they can’t disappear off-screen.
* Powerup: Collectable/consumable items that can be randomly found from enemies.
   * Health (much more common than others)
   * Big Bomb (does heavy damage to everything on screen)
   * Time stop for 5s
   * Possibly others later, or depending on character.
* Magenta: The color Magenta signals danger; something the player must avoid. #ff0099
* Miniboss: Special monsters that drop a Treasure Chest upon death. They are much tougher than normal monsters. Some might have unique Telegraphed attacks. There should be around 2 per Stage.
* Boss: A special extra-tough monster at the end of a stage, with unique Telegraphed attacks. Drops a Treasure Chest but no Exp.
* Final Boss: The final Boss of the final Stage in an Adventure. Does not drop a Treasure Chest or Exp.
* Telegraph: Special attacks are telegraphed in Magenta. The player should have enough time to move out of the way.
* Enemy Projectiles: Enemy projectiles that can hurt the player must always be Magenta.
* Treasure Chest: A treasure box that grants the rewards of a level-up when picked up. It does not actually increase the player’s level. Each Stage should have around 3. If at least one weapon is able to be Evolved (it is max level and has an evolution), then the option to Evolve the weapon should appear as the first option, replacing one of the level-up option slots. If multiple weapons can be evolved, then it chooses a random one to appear there. If a weapon has multiple possible evolutions, it chooses a random evolution option to appear for it.
* Exp Orb: Collectable objects that dropped from dead monsters. Fills up the character experience bar. Has a color/size based on how much exp it fills up.
* Level Up: When a player’s experience (aka: exp or xp) bar fills up, they level up. Each level up takes more xp than the last. When they level up, they can choose one of up to three randomized Level-Up Options. The player should level up roughly once every 30 seconds (average of 10 times in a stage).
* Level-Up Options:
   * Get a new weapon, randomly chosen when the option appears (if there’s enough space for a new one)
   * Upgrade the level of a weapon, randomly chosen when the option appears (if possible).
   * Alternate options based on the characters in the party.
* Party: A party is made up of up to three characters. One character is the Leader. The Leader is the one whom the player will play as. The player starts the Adventure with the Leader’s Starting Weapon. The other two are Supports. Having a character in a party (whether they are Leader or not) has the following effects:
   * Weapons related to that character will appear in Level-Up Options; generally a pool of around five. For example, the Warrior has martial weapons such as Greatsword, Whip, Throwing Hammer, Crossbow, Flail, and Tower Shield.
   * The character’s Unique Meta-Mechanic(s) will be active during the Adventure. For example, the Warrior adds a new button on the bottom-right that lets you fire all of your weapons exactly once, ignoring (and not affecting) their cooldowns (.2 second delay between firing each weapon). This ability has a 30-second cooldown.
* Character: Characters have four things:
   * Their Default Weapon, the weapon that the player starts with if the character is their Leader at the start of an Adventure.
   * Their Weapon Pool, the weapons added to the pool of available random weapons when the character is in the party. Characters’ Weapon Pools can overlap, and the weapon is only added once to the pool of available random weapons.
   * Their Unique Meta-Mechanic, a special mechanic that is active when the character is in the party.
   * Their Passive Training, a stat bonus that is ALWAYS active whether or not the character is in their current party. This stat bonus can be upgraded in the character list, but not during an Adventure.
* Unlocking Characters: The player starts with only the Explorer unlocked. The player will unlock Warrior and Rogue in the Starting Adventure.
* Starting Adventure: Instead of the normal flow, the player will be put into the Starting Adventure the first time they open the game and hit Play. This is a bespoke Adventure specially-crafted to not be too difficult. If they die on any stage in this Adventure, they go into a special Loss screen where the only option is to restart at the beginning of that stage.






Gameplay and Controls
* The game can only be played vertically. 
* The player can press their finger on the screen and drag in a direction. The character will move in that same direction. Once the player releases their finger, the character will stop moving. There should be a visual for this (a “virtual joystick”)
* For testing on PC, players can use WASD as alternate movement controls.
* Phone vibrates slightly when the player takes damage
* The game will temporarily pause when the player levels up or picks up a Treasure Chest
* Each Weapon on the player will automatically activate at regular intervals. If a player has multiple copies of the same weapon, they should be staggered so their projectiles/AoEs/etc don’t overlap.
* Weapons that have auto targeting feature prioritize the nearest monster.
* The effects of powerups do not stack (if applicable). Its duration will be reset to full when picking up two of the same ones (if applicable).
Game Flow Between Screens
* Main Menu - The game starts here. Has a title, a Play button, and a settings button. This should play the game’s main theme. Tapping Play goes into the active Adventure if one is active, or it goes into the Character Select screen.
* Character Select - Shows the list of unlocked characters you can play as. Tapping one shows you their stats, with a button to let you add that character to your party as the Leader or one of the Supports. There is a button to go to the Start Adventure screen. There should be a warning if you try to start an adventure with less than a full Party.
* Start Adventure lets you choose an Adventure to play. It is styled like a bounty board. There are three random Adventures available at a time. You can reroll the three Adventures with a button). Tapping one lets you see the details, with a button to start the adventure.
* Adventure Beat - When starting an Adventure, and between Stages in the Adventure, you get an Adventure Beat, which lets you choose from options (detailed in the Adventure section above). Generally there are two options, each with an image, the action you could take, and the effect.
* Story Beat - Sometimes during an adventure (often before the Adventure Beat), the characters will talk to each other. This should appear like a visual novel, with characters’ portraits appearing on screen when they’re in the scene/talking. There should be a proper background.
* Stage - This is the core gameplay. Survivorlike/bullet heaven. Upon Victory, go to the Victory screen. Upon defeat, go to the Defeat screen.
* Victory - The player won. Show damage stats, enemies defeated, etc. Celebratory and juicy. There’s a button to continue the Adventure. Unless it’s the last Stage, then instead it goes into Adventure End.
* Defeat - The player lost. Same information as the Victory screen, but more melancholy. There’s a button that takes you to the Adventure End screen.
* Adventure End - Shows all of the stats from the Adventure. It is celebratory if the Adventure was successful.
Progression - Quests and Rewards (WIP, DO NOT IMPLEMENT)
* TBD


Environment - In-Match
        In a match, the world is constantly moving down (such that it feels like the player is moving up). The world is made up of square 32x32 tiles. The left and right sides of the screen are bounded by walls made up of tiles as well. The ground tiles chosen should be randomized. Each biome has its own tileset.


Aesthetic
* Clean bold readable fantasy 2D style
* 2D with effects such as glow and (fake) lighting
* Each zone will feature monsters of the same theme/flavor
* Most non-boss monsters do not have projectile base abilities, they only move either straight downward, or (more rarely) towards the player.
* The stage scrolls downward infinitely, making it appear like the player is marching forward (up).
Endgame (WIP, DO NOT IMPLEMENT)
* Secret dungeons (do not implement)
* Unlocks/collections (do not implement)
* Achievements/challenges (do not implement)
* Additional game mode (do not implement)
New User Flow (Starting Adventure)
        Fresh start
-The player launches the app and enters the main menu, which features the Play and Options buttons.
-The player taps on Play, which takes them straight into the special “Who Are You?” page, where they can choose either the male or female Explorer as their character.
- After choosing their character, players are greeted with a quick Story Beat:
        - “Hearing tales of gold and glory, you set off to the wild North! Go forth, Explorer. The Realm will one day sing the tales of your grand adventure!”
- Players start the first Stage (Grasslands) as Explorer. We want a new player to get right into the action.
- If the player dies, they go into a special Defeat screen that only has the option to restart the stage.
- After the first stage, there is another Story Beat, introducing the Warrior, who heard that there’s treasure in a nearby dungeon. You both agree to travel together. This unlocks the Warrior and adds her as a Support in your party.
- Next stage (Forest)
- After the stage, there is another Story Beat, introducing the shifty Rogue, who tries to steal your stuff. The Warrior stops him, and eventually the Rogue decides to go with you to the dungeon, but you don’t trust him. Unlocks Rogue and puts him as the second Support in your party.
- Next stage (Cobblestone Path)
- After the stage, another Story beat. You reach the entrance to the dungeon. The Rogue proves himself useful, unlocking the sealed door.
- Next stage (Dungeon)
- Story beat with characterization; there’s a mysterious evil in this dungeon.
- Next stage (Deep Dungeon, with final boss: Skeleton Commander)
- After the Victory screen, but before the Adventure End screen, there’s a Story Beat. You guys decide to form a party together.
        








Playable Characters
Explorer:
Appearance:
* Normal man/woman (depending on which one the player chose), brown hair
* Bright-eyed with a bright attitude
* A belt with lots of trinkets including a spyglass
Weapons:
* Shuriken - Starting Weapon - Shoots straight forward, piercing enemies
* Wand of Fireball - AoE
* Acid Potion - Throw randomly near a random enemy; makes temporary AoE ground puddles
* Holy Symbol - Constant AoE around character
* Crossbow - Fires at the closest enemy; high damage with a long cooldown
Meta-Mechanic:
* None for now (they’ll gain one in the story later, but it’s unimplemented for now)
Passive Training:
* Determination (+15% max health per level)


Warrior:
Appearance:
* Female, gruff veteran soldier with a face scar, auburn hair
* Armor that has taken some hits, but is still colorful with a noticeable coat of arms
Weapons:
* Greatsword - Starting Weapon - High damage sweep in front of character
* Whip - Alternates between a long-range oval attack toward the nearest enemy, and an aoe sweep attack slightly larger than Greatsword
* Throwing Hammer - Throws upwards in an arc (falls to the bottom of the screen)
* Flail - Rotating spiky metal ball around character
* Crossbow
* Tower Shield - If hit, negates the damage and does a horizontal oval AoE in front of the character that knocks enemies back. The AoE also deflects projectiles back at enemies.  Starts on a 45-second cooldown. Cooldown is visible to player on their character.
Meta-Mechanic:
* Adds a button to fire all weapons at once (with a .2 second delay between each). This ignores their cooldowns and does not affect their cooldowns. This ability has a 30-second cooldown.
Passive Training:
* Weapon Mastery (+15% damage per level)
Rogue:
Appearance:
* Male, shifty-eyed with a hood, light gray hair
* Arms with visible scars and muscles
* Sleeveless with cloak
Weapons:
* Throwing Knives - Starting Weapon - Shoots three straight forward in a fan.
* Shuriken - Shoots straight forward, piercing enemies
* Acid Potion - Throw randomly near a random enemy; makes temporary AoE ground puddles
* Crossbow
* Smoke Bomb - Gives brief invincibility to the player when they’re hit, and damages enemies around them. Starts on a 30-second cooldown. Cooldown is visible to player on their character.
Meta-Mechanics:
* New Level-up Option: Apply Poison to X
   * X is a random weapon the player has that isn’t already Poisoned
   * When the weapon hits, it poisons the target for five seconds, dealing damage over time (20% of the weapon’s damage total).
   * If the target is already poisoned, it adds a new stack. All stacks on the target deal damage.
* Poison Bombs appear as random pickups. These randomly drop in addition to regular pickups, so they won’t dilute the pool. Picking one up throws it at a random enemy on screen, which makes a large poison cloud appear, dealing damage over time.
Passive Training:
* Magnet (+15% pickup range per level)